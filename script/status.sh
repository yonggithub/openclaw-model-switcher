#!/bin/bash
cd $(dirname $0)/..
if [ -f "app.pid" ]; then
    if ps -p $(cat app.pid) > /dev/null; then
        echo "OpenClawSwitch is running."
    else
        echo "OpenClawSwitch is not running, but pid file exists."
    fi
else
    echo "OpenClawSwitch is not running."
fi