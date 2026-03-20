#!/bin/bash
cd $(dirname $0)/..
export PATH=/home/john/go-local/go/bin:$PATH
nohup ./build/OpenClawSwitch-linux >/dev/null 2>&1 &
echo $! > app.pid
echo "OpenClawSwitch started."