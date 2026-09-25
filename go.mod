module github.com/HMuSeaB/wbmux

// 不能低于 1.24：Go 1.22 的链接器不给 darwin/arm64 二进制写 LC_UUID，
// 而 macOS 26 的 dyld 要求这个 load command，缺了会直接 abort trap。
// 实测 go1.22 无、go1.24 有。详见 .github/workflows/ci.yml 的说明。
go 1.24
