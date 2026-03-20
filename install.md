

$env:Path = "C:\Users\81274664.CORPHQ-HK-PCCW\Downloads\go1.26.1.windows-amd64\go\bin;" + $env:Path
$env:GOPROXY = "https://goproxy.cn,direct"
$env:GOPROXY="http://localhost:7897,direct"
cd c:\Code\openclawfilegenerate
# 编译 Linux 版
$env:GOOS="linux"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
go build -ldflags "-s -w" -o voiceprompt-sync .
# 编译 Windows 版
cd c:\Code\openclawfilegenerate
# 下载依赖
go mod tidy
# 编译主程序
go build -o openclawswitch.exe .
# 编译 CLI 工具
go build -o update_openclaw.exe ./cmd/update/
# 运行（监听 0.0.0.0:6789）
./openclawswitch.exe

$env:GOOS="windows"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
go build -ldflags "-s -w" -o openclawswitchc.exe .

go mod tidy
go build -o openclawswitch.exe .