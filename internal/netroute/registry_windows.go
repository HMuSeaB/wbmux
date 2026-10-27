//go:build windows

package netroute

import (
	"strings"
	"syscall"
	"unsafe"
)

const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// systemProxy 读注册表里的系统代理；没开启时返回空串。
//
// 用注册表 API 而**不是** `reg query`：wbmux 是常驻 GUI，某些安全软件会
// 拦子进程执行，命令行工具还多一次进程创建（约 20ms）。这个函数每次
// 启动都会调，不值得。
//
// 返回值第二个是 ProxyOverride（"绕过代理"清单），交给 bypassFunc 用。
func systemProxy() (string, string) {
	k, err := regOpenKey(internetSettingsKey)
	if err != nil {
		return "", ""
	}
	defer syscall.RegCloseKey(k)

	if regDWORD(k, "ProxyEnable") == 0 {
		return "", ""
	}

	server, _ := regString(k, "ProxyServer")
	if server == "" {
		return "", ""
	}
	override, _ := regString(k, "ProxyOverride")

	// ProxyServer 可能是 httptest 那样"直接一个地址"，也可能是
	// "http=host:port;https=host:port"。取 https，没有就取 http，再没有
	// 取第一个非空项。
	if idx := strings.IndexByte(server, '='); idx >= 0 {
		parts := strings.Split(server, ";")
		var httpV, anyV string
		for _, p := range parts {
			i := strings.IndexByte(p, '=')
			if i < 0 {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(p[:i]))
			val := strings.TrimSpace(p[i+1:])
			if val == "" {
				continue
			}
			if anyV == "" {
				anyV = val
			}
			if name == "http" {
				httpV = val
			}
		}
		if httpV != "" {
			server = httpV
		} else if anyV != "" {
			server = anyV
		}
	}

	return normalizeProxyAddr(strings.TrimSpace(server)), override
}

func regOpenKey(path string) (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var h syscall.Handle
	err = syscall.RegOpenKeyEx(syscall.HKEY_CURRENT_USER, p, 0, syscall.KEY_READ, &h)
	return h, err
}

func regDWORD(h syscall.Handle, name string) uint32 {
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	var val uint32
	var typ, size uint32 = 0, uint32(unsafe.Sizeof(val))
	err = syscall.RegQueryValueEx(h, n, nil, &typ, (*byte)(unsafe.Pointer(&val)), &size)
	if err != nil {
		return 0
	}
	if typ != syscall.REG_DWORD && typ != syscall.REG_DWORD_BIG_ENDIAN {
		return 0
	}
	return val
}

func regString(h syscall.Handle, name string) (string, error) {
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return "", err
	}
	var typ, size uint32
	// 先问大小，再分配：代理地址虽短，但 ProxyOverride 可以很长。
	if err := syscall.RegQueryValueEx(h, n, nil, &typ, nil, &size); err != nil {
		return "", err
	}
	if size == 0 {
		return "", nil
	}
	buf := make([]uint16, size/2+1)
	if err := syscall.RegQueryValueEx(h, n, nil, &typ, (*byte)(unsafe.Pointer(&buf[0])), &size); err != nil {
		return "", err
	}
	return syscall.UTF16ToString(buf), nil
}

func normalizeProxyAddr(v string) string {
	if v == "" {
		return ""
	}
	lower := strings.ToLower(v)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "socks5://") {
		return v
	}
	return "http://" + v
}
