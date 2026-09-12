package proxmox

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// FactoryImageProvider is optional: manual ISO providers retain their existing
// contract. DownloadISO returns before waiting so the job can persist its UPID.
type FactoryImageProvider interface {
	PreflightFactoryMachines(context.Context, []MachineSpec, string) error
	DownloadISO(context.Context, string, string, string, string) (string, error)
	VerifyISO(context.Context, string, string) error
}

var ErrImageDownloadUncertain = errors.New("Factory ISO download outcome is uncertain; inspect provider tasks before retrying")

var factoryISOURL = regexp.MustCompile(`^https://factory\.talos\.dev/image/[a-f0-9]{64}/v[0-9]+\.[0-9]+\.[0-9]+/metal-amd64\.iso$`)
var ownedISOName = regexp.MustCompile(`^talosdeck-[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}\.iso$`)
var isoChecksum = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (c *Client) PreflightFactoryMachines(ctx context.Context, machines []MachineSpec, storage string) error {
	if !resourceName.MatchString(storage) {
		return errors.New("ISO storage is required")
	}
	if err := c.preflightMachines(ctx, machines, true); err != nil {
		return err
	}
	var response struct {
		Data struct {
			Content string `json:"content"`
			Avail   uint64 `json:"avail"`
			Active  int    `json:"active"`
			Enabled int    `json:"enabled"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, "/nodes/"+url.PathEscape(c.cfg.Node)+"/storage/"+url.PathEscape(storage)+"/status", &response); err != nil {
		return errors.New("cannot verify ISO storage")
	}
	supportsISO := false
	for _, content := range strings.Split(response.Data.Content, ",") {
		supportsISO = supportsISO || strings.TrimSpace(content) == "iso"
	}
	if !supportsISO || response.Data.Active != 1 || response.Data.Enabled != 1 {
		return errors.New("ISO storage must be active, enabled and support ISO content")
	}
	if response.Data.Avail < 2<<30 {
		return errors.New("ISO storage needs at least 2 GiB available")
	}
	return nil
}
func (c *Client) isoExists(ctx context.Context, storage, filename string) (bool, error) {
	var response struct {
		Data []struct {
			Volid string `json:"volid"`
			Size  uint64 `json:"size"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, "/nodes/"+url.PathEscape(c.cfg.Node)+"/storage/"+url.PathEscape(storage)+"/content?content=iso", &response); err != nil {
		return false, errors.New("cannot verify prepared ISO")
	}
	for _, item := range response.Data {
		if item.Volid == storage+":iso/"+filename {
			return true, nil
		}
	}
	return false, nil
}
func (c *Client) DownloadISO(ctx context.Context, storage, filename, sourceURL, checksum string) (string, error) {
	if !resourceName.MatchString(storage) || !ownedISOName.MatchString(filename) || !factoryISOURL.MatchString(sourceURL) || !isoChecksum.MatchString(checksum) {
		return "", errors.New("invalid Factory ISO request")
	}
	exists, err := c.isoExists(ctx, storage, filename)
	if err != nil {
		return "", err
	}
	if exists {
		return "", errors.New("prepared ISO filename already exists; refusing overwrite")
	}
	form := url.Values{"content": {"iso"}, "filename": {filename}, "url": {sourceURL}, "checksum": {checksum}, "checksum-algorithm": {"sha256"}, "verify-certificates": {"1"}}
	var response struct {
		Data string `json:"data"`
	}
	if err := c.postForm(ctx, "/nodes/"+url.PathEscape(c.cfg.Node)+"/storage/"+url.PathEscape(storage)+"/download-url", form, &response); err != nil {
		return "", ErrImageDownloadUncertain
	}
	if response.Data == "" {
		return "", ErrImageDownloadUncertain
	}
	return response.Data, nil
}
func (c *Client) VerifyISO(ctx context.Context, storage, filename string) error {
	exists, err := c.isoExists(ctx, storage, filename)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("download task finished without expected ISO")
	}
	return nil
}
