use crate::proto::{self, Identity, ProtocolVersion, SignedPayload};
use crate::utils;
use chacha20poly1305::aead::Aead;
use chacha20poly1305::{ChaCha20Poly1305, KeyInit};
use ed25519_dalek::{Signature, Signer, SigningKey, VerifyingKey};
use futures::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use hkdf::Hkdf;
use sha2::Sha256;
use std::io;
use x25519_dalek::{EphemeralSecret, PublicKey};

const NONCE_SIZE: usize = 12;
const SESSION_KEY_SIZE: usize = 32;
const MAX_RAW_PACKET_SIZE: usize = 1 << 26; // 64MB

const CLIENT_KEY_INFO: &[u8] = b"RDSEC_KEY_CLIENT";
const SERVER_KEY_INFO: &[u8] = b"RDSEC_KEY_SERVER";

#[derive(Clone)]
pub struct Credential {
    signing_key: SigningKey,
    id: String,
}

impl Credential {
    /// Create a new credential with a random key
    pub fn new() -> Self {
        let signing_key = SigningKey::generate(&mut rand_core::OsRng);
        let id = hex::encode(signing_key.verifying_key().as_bytes());

        Self { signing_key, id }
    }

    /// Get the credential ID
    pub fn id(&self) -> &str {
        &self.id
    }

    /// Get the public key
    pub fn public_key(&self) -> Vec<u8> {
        self.signing_key.verifying_key().as_bytes().to_vec()
    }

    /// Sign data
    pub fn sign(&self, data: &[u8]) -> Vec<u8> {
        self.signing_key.sign(data).to_bytes().to_vec()
    }

    /// Get Identity message
    pub fn identity(&self) -> Identity {
        Identity {
            id: self.id.clone(),
            public_key: self.public_key(),
        }
    }
}

/// Verify a signed payload
fn verify_signature(identity: &Identity, signed: &SignedPayload) -> Result<(), io::Error> {
    let public_key = VerifyingKey::from_bytes(
        identity
            .public_key
            .as_slice()
            .try_into()
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "invalid public key"))?,
    )
    .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "invalid public key"))?;

    let signature = Signature::from_bytes(
        signed
            .signature
            .as_slice()
            .try_into()
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "invalid signature"))?,
    );

    public_key
        .verify_strict(&signed.data, &signature)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "signature verification failed"))
}

/// Derive session keys using HKDF
fn derive_key(shared_secret: &[u8], salt: &[u8], info: &[u8]) -> [u8; SESSION_KEY_SIZE] {
    let hkdf = Hkdf::<Sha256>::new(Some(salt), shared_secret);
    let mut key = [0u8; SESSION_KEY_SIZE];
    hkdf.expand(info, &mut key).expect("HKDF expand failed");
    key
}

/// Increment nonce for the next message
fn increment_nonce(nonce: &mut [u8]) {
    for byte in nonce.iter_mut().rev() {
        *byte = byte.wrapping_add(1);
        if *byte != 0 {
            break;
        }
    }
}

/// Secure connection with encryption
pub struct SecureConnection<T> {
    conn: T,
    encryptor: ChaCha20Poly1305,
    decryptor: ChaCha20Poly1305,
    encrypt_nonce: [u8; NONCE_SIZE],
    decrypt_nonce: [u8; NONCE_SIZE],
}

impl<T: AsyncRead + AsyncWrite + Unpin> SecureConnection<T> {
    /// Perform client-side handshake
    pub async fn client_handshake(
        mut conn: T,
        credential: &Credential,
        alpn: &str,
    ) -> io::Result<Self> {
        // Generate ephemeral key pair
        let ephemeral_secret = EphemeralSecret::random_from_rng(&mut rand_core::OsRng);
        let ephemeral_public = PublicKey::from(&ephemeral_secret);

        // Create ClientInitPayload
        let client_nonce = utils::random_bytes(NONCE_SIZE);
        let timestamp = utils::unix_timestamp();

        let client_init = proto::ClientInitPayload {
            version: ProtocolVersion::ProtocolVersion1 as i32,
            nonce: client_nonce.clone(),
            timestamp,
            identity: Some(credential.identity()),
            alpn: alpn.to_string(),
            session_public_key: ephemeral_public.as_bytes().to_vec(),
        };

        // Sign and send
        let payload = proto::encode_message(&client_init);
        let signature = credential.sign(&payload);
        let signed = SignedPayload {
            data: payload,
            signature,
        };

        write_length_prefixed(&mut conn, &proto::encode_message(&signed)).await?;

        // Read server init
        let server_init_bytes = read_length_prefixed(&mut conn).await?;
        let server_signed: SignedPayload = proto::decode_message(&server_init_bytes)
            .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))?;

        let server_init: proto::ServerInitPayload = proto::decode_message(&server_signed.data)
            .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))?;

        // Validate server init
        if server_init.version != ProtocolVersion::ProtocolVersion1 as i32 {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "invalid protocol version",
            ));
        }

        let server_identity = server_init
            .identity
            .as_ref()
            .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidData, "missing identity"))?;

        verify_signature(server_identity, &server_signed)?;

        // Derive session keys
        let server_public = PublicKey::from(
            <[u8; 32]>::try_from(server_init.session_public_key.as_slice()).map_err(|_| {
                io::Error::new(io::ErrorKind::InvalidData, "invalid server public key")
            })?,
        );

        let shared_secret = ephemeral_secret.diffie_hellman(&server_public);

        // Client encrypts with CLIENT_KEY_INFO, server decrypts with it
        let mut client_salt = client_nonce.clone();
        client_salt.extend_from_slice(&server_init.nonce);
        let encrypt_key = derive_key(shared_secret.as_bytes(), &client_salt, CLIENT_KEY_INFO);

        // Server encrypts with SERVER_KEY_INFO, client decrypts with it
        let mut server_salt = server_init.nonce.clone();
        server_salt.extend_from_slice(&client_nonce);
        let decrypt_key = derive_key(shared_secret.as_bytes(), &server_salt, SERVER_KEY_INFO);

        Ok(Self {
            conn,
            encryptor: ChaCha20Poly1305::new(&encrypt_key.into()),
            decryptor: ChaCha20Poly1305::new(&decrypt_key.into()),
            encrypt_nonce: client_nonce.as_slice().try_into().unwrap(),
            decrypt_nonce: server_init.nonce.as_slice().try_into().unwrap(),
        })
    }

    /// Read decrypted data
    pub async fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        // Read length-prefixed encrypted message
        let encrypted_msg = read_length_prefixed(&mut self.conn).await?;

        let encrypted_data: proto::EncryptedData = proto::decode_message(&encrypted_msg)
            .map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))?;

        // Validate nonce size
        if encrypted_data.nonce.len() != NONCE_SIZE {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "invalid nonce size",
            ));
        }

        // Verify nonce matches our expected counter value
        let received_nonce: &[u8; NONCE_SIZE] = encrypted_data.nonce.as_slice().try_into().unwrap();
        if received_nonce != &self.decrypt_nonce {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "nonce mismatch - possible replay attack",
            ));
        }

        // Decrypt using our tracked nonce
        let decrypted = self
            .decryptor
            .decrypt((&self.decrypt_nonce).into(), encrypted_data.payload.as_slice())
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "decryption failed"))?;

        // Increment decrypt nonce for next message
        increment_nonce(&mut self.decrypt_nonce);

        // Copy to buffer
        let to_copy = buf.len().min(decrypted.len());
        buf[..to_copy].copy_from_slice(&decrypted[..to_copy]);

        Ok(to_copy)
    }

    /// Write encrypted data
    pub async fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        // Increment nonce
        increment_nonce(&mut self.encrypt_nonce);

        // Encrypt
        let encrypted = self
            .encryptor
            .encrypt((&self.encrypt_nonce).into(), buf)
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "encryption failed"))?;

        // Create EncryptedData message
        let encrypted_data = proto::EncryptedData {
            nonce: self.encrypt_nonce.to_vec(),
            payload: encrypted,
        };

        // Send length-prefixed
        let msg = proto::encode_message(&encrypted_data);
        write_length_prefixed(&mut self.conn, &msg).await?;

        Ok(buf.len())
    }
}

/// Write length-prefixed data
async fn write_length_prefixed<W: AsyncWrite + Unpin>(
    writer: &mut W,
    data: &[u8],
) -> io::Result<()> {
    let len = data.len() as u32;
    writer.write_all(&len.to_be_bytes()).await?;
    writer.write_all(data).await?;
    writer.flush().await?;
    Ok(())
}

/// Read length-prefixed data
async fn read_length_prefixed<R: AsyncRead + Unpin>(reader: &mut R) -> io::Result<Vec<u8>> {
    let mut len_buf = [0u8; 4];
    reader.read_exact(&mut len_buf).await?;
    let len = u32::from_be_bytes(len_buf) as usize;

    if len > MAX_RAW_PACKET_SIZE {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "packet too large",
        ));
    }

    let mut data = vec![0u8; len];
    reader.read_exact(&mut data).await?;

    Ok(data)
}
