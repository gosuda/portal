#!/bin/bash
# Build script for RelayDNS WASM client
# Builds the WASM module and copies output to SDK directory

set -e

echo "🔨 Building RelayDNS WASM client..."

# Build with wasm-pack
wasm-pack build --target web --out-dir pkg

echo "📦 Build complete!"

# Create SDK output directory if it doesn't exist
SDK_DIR="../../sdk/wasm"
mkdir -p "$SDK_DIR"

echo "📋 Copying files to SDK directory: $SDK_DIR"

# Copy WASM output files
cp pkg/relaydns_wasm.js "$SDK_DIR/"
cp pkg/relaydns_wasm_bg.wasm "$SDK_DIR/"
cp pkg/relaydns_wasm.d.ts "$SDK_DIR/"
cp pkg/package.json "$SDK_DIR/"

# Copy README if exists
if [ -f "README.md" ]; then
    cp README.md "$SDK_DIR/"
fi

echo "✅ WASM files copied to $SDK_DIR"
echo ""
echo "📁 Output files:"
ls -lh "$SDK_DIR"

echo ""
echo "🎉 Build and copy complete!"
echo ""
echo "You can now use the WASM module from: $SDK_DIR"
