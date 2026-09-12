package proxmox

// PrivateConfig is used only when migrating credentials to encrypted storage.
// It must never be serialized into an API response or audit event.
func (c *Client) PrivateConfig() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}
