// This module contains generated protobuf code
// Generated files will be placed here by build.rs

// Include generated proto modules separately to avoid conflicts
#[allow(dead_code)]
pub mod rdverb {
    include!(concat!(env!("OUT_DIR"), "/rdverb.rs"));
}

#[allow(dead_code)]
pub mod rdsec {
    include!(concat!(env!("OUT_DIR"), "/rdsec.rs"));
}

// Re-export commonly used types
pub use rdverb::*;
pub use rdsec::*;

use prost::Message;
use std::io;

/// Helper to encode a protobuf message
pub fn encode_message<M: Message>(msg: &M) -> Vec<u8> {
    let mut buf = Vec::with_capacity(msg.encoded_len());
    msg.encode(&mut buf).expect("failed to encode message");
    buf
}

/// Helper to decode a protobuf message
pub fn decode_message<M: Message + Default>(data: &[u8]) -> Result<M, prost::DecodeError> {
    M::decode(data)
}


/// Async version: write packet
pub async fn write_packet_async<W: futures::io::AsyncWrite + Unpin>(
    writer: &mut W,
    packet: &Packet,
) -> io::Result<()> {
    use futures::io::AsyncWriteExt;

    let payload = encode_message(packet);
    let len = payload.len() as u32;

    writer.write_all(&len.to_be_bytes()).await?;
    writer.write_all(&payload).await?;
    writer.flush().await?;
    Ok(())
}

/// Async version: read packet
pub async fn read_packet_async<R: futures::io::AsyncRead + Unpin>(
    reader: &mut R,
) -> io::Result<Packet> {
    use futures::io::AsyncReadExt;

    let mut len_buf = [0u8; 4];
    reader.read_exact(&mut len_buf).await?;
    let len = u32::from_be_bytes(len_buf) as usize;

    // Packet size limit (64MB)
    const MAX_PACKET_SIZE: usize = 1 << 26;
    if len > MAX_PACKET_SIZE {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "packet too large",
        ));
    }

    let mut payload = vec![0u8; len];
    reader.read_exact(&mut payload).await?;

    decode_message(&payload).map_err(|e| {
        io::Error::new(io::ErrorKind::InvalidData, e)
    })
}
