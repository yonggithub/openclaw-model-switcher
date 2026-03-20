#!/bin/bash
export PATH=$HOME/go-local/go/bin:$PATH
cd $(dirname $0)/..
echo "Building OpenClawSwitch for Linux..."
GOOS=linux GOARCH=amd64 go build -buildvcs=false -o ./build/OpenClawSwitch-linux .
if [ $? -eq 0 ]; then
    echo "Linux build successful: OpenClawSwitch-linux"
else
    echo "Linux build failed."
fi