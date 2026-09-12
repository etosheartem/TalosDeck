package auth

import "strings"

func normalizedPath(path string) string {
	path = strings.TrimSuffix(path, "/")
	if strings.HasPrefix(path, "/api/clusters/") {
		rest := strings.TrimPrefix(path, "/api/clusters/")
		_, suffix, found := strings.Cut(rest, "/")
		if !found {
			return "/api/cluster"
		}
		if strings.HasPrefix(suffix, "ws/") {
			return "/" + suffix
		}
		return "/api/" + suffix
	}
	return path
}

// Can is the shared REST, download and WebSocket role policy. Jobs additionally
// call CanJob with their immutable request kind, including stop/review requests.
func Can(role, method, path string) bool {
	if !ValidRole(role) {
		return false
	}
	if role == "admin" {
		return true
	}
	path = normalizedPath(path)
	if strings.HasPrefix(path, "/api/auth/users") || strings.HasPrefix(path, "/api/providers") || strings.HasPrefix(path, "/api/proxmox") || strings.HasPrefix(path, "/api/auth/config") {
		return false
	}
	if strings.Contains(path, "/download") && (strings.Contains(path, "/backups") || strings.Contains(path, "/support")) {
		return false
	}
	if strings.HasPrefix(path, "/api/notifications/") {
		return (method == "GET" || method == "HEAD") && (path == "/api/notifications/status" || (role == "operator" && path == "/api/notifications/deliveries"))
	}
	if role == "operator" && (path == "/api/alerts/silences" && method == "POST" || strings.HasPrefix(path, "/api/alerts/silences/") && method == "DELETE") {
		return true
	}
	if method == "GET" || method == "HEAD" {
		for _, prefix := range []string{"/api/clusters", "/api/cluster", "/api/nodes", "/api/k8s", "/api/storage", "/api/backups", "/api/audit", "/api/jobs", "/api/alerts", "/api/diagnostics", "/api/certificates", "/api/health", "/api/events", "/api/network", "/ws/nodes", "/api/auth/me", "/api/auth/providers"} {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return true
			}
		}
		return false
	}
	if path == "/api/auth/logout" || path == "/api/auth/password" {
		return method == "POST"
	}
	if role != "operator" {
		return false
	}
	if method == "POST" {
		if path == "/api/diagnostics/run" {
			return true
		}
		if path == "/api/jobs" || path == "/api/jobs/plan" || strings.HasPrefix(path, "/api/jobs/") {
			return true
		}
		if path == "/api/backups" || path == "/api/backups/etcd" || path == "/api/backups/create" {
			return true
		}
		if strings.HasPrefix(path, "/api/nodes/") {
			for _, suffix := range []string{"/reboot", "/cordon", "/uncordon", "/drain", "/maintenance", "/restart"} {
				if strings.HasSuffix(path, suffix) {
					return true
				}
			}
		}
	}
	return false
}

func CanJob(role, kind string) bool {
	if role == "admin" {
		return true
	}
	if role != "operator" {
		return false
	}
	switch kind {
	case "talos-upgrade", "kubernetes-upgrade", "rolling-reboot", "backup-create", "backup-etcd", "etcd-backup", "diagnostics":
		return true
	}
	return false
}
func Permissions(role string) []string {
	result := []string{}
	if ValidRole(role) {
		result = append(result, "read", "logs", "jobs.read", "backups.read", "audit.read")
	}
	if role == "operator" || role == "admin" {
		result = append(result, "nodes.operate", "upgrades.run", "backups.create")
	}
	if role == "admin" {
		result = append(result, "clusters.manage", "config.write", "providers.manage", "users.manage", "backups.download", "backups.restore", "settings.manage")
	}
	return result
}
