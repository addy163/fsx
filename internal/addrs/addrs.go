package addrs

import "net"

// GlobalV6 返回本机可用于公网直连的全局 IPv6 地址，
// 排除回环、链路本地与 ULA（fc00::/7，不可公网路由）。
func GlobalV6() []string {
	var out []string
	seen := map[string]bool{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		as, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range as {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP
			if ip.To4() != nil || !ip.IsGlobalUnicast() || ip.IsLinkLocalUnicast() || ip.IsPrivate() {
				continue
			}
			s := ip.String()
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}