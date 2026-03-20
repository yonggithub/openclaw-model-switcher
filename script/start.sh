#!/bin/bash
cd $(dirname $0)/..
nohup ./OpenClawSwitch-linux >/dev/null 2>&1 &
echo $! > app.pid
echo "OpenClawSwitch started."