#!/bin/bash
cd $(dirname $0)/..
if [ -f "app.pid" ]; then
    kill $(cat app.pid)
    rm app.pid
    echo "OpenClawSwitch stopped."
else
    echo "OpenClawSwitch is not running."
fi