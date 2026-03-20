#!/bin/bash
export PATH=$HOME/go-local/go/bin:$PATH
cd $(dirname $0)/..
echo "Building update_openclaw for Windows..."
GOOS=windows GOARCH=amd64 go build -buildvcs=false -o ./build/update_openclaw.exe ./cmd/update/
if [ $? -eq 0 ]; then
    echo "Windows build successful: update_openclaw.exe"
else
    echo "Windows build failed."
fi