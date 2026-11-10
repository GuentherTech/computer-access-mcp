package main

import (
	"errors"
	"github.com/GuentherTech/computer-access-mcp/internal/link"
	"strings"
)

func (a *agent) saveDesktopService(id, name string) error {
	return a.mutate(func(c *agentConfig) error {
		if id == "" {
			if len(c.Services) >= 32 {
				return errors.New("maximum 32 services per PC")
			}
			s := newService(name)
			if err := validateService(s); err != nil {
				return err
			}
			c.Services = append(c.Services, s)
			return nil
		}
		for i := range c.Services {
			if c.Services[i].ID == id {
				c.Services[i].Name = strings.TrimSpace(name)
				return validateService(c.Services[i])
			}
		}
		return errors.New("service not found")
	})
}

func (a *agent) desktopServiceAction(id, action string) error {
	return a.mutate(func(c *agentConfig) error {
		for i := range c.Services {
			if c.Services[i].ID == id {
				switch action {
				case "power":
					c.Services[i].Enabled = !c.Services[i].Enabled
				case "url":
					c.Services[i].URLID = link.NewID()
				case "password":
					c.Services[i].Password = link.NewSecret()
				case "delete":
					c.Services = append(c.Services[:i], c.Services[i+1:]...)
				default:
					return errors.New("unknown service action")
				}
				return nil
			}
		}
		return errors.New("service not found")
	})
}

func (a *agent) saveDesktopConnection(mode, name, relayURL, key, cloudflared string) error {
	if mode != "relay" && mode != "cloudflare" {
		return errors.New("invalid connection mode")
	}
	if strings.TrimSpace(name) == "" || len(name) > 100 {
		return errors.New("device name must contain 1 to 100 characters")
	}
	var base string
	if mode == "relay" {
		var err error
		base, err = link.BaseURL(relayURL)
		if err != nil {
			return err
		}
	}
	return a.mutate(func(c *agentConfig) error {
		if mode == "relay" && c.RelayURL != "" && c.RelayURL != base {
			return errors.New("the relay address is fixed to preserve your URLs")
		}
		c.Mode = mode
		c.Name = strings.TrimSpace(name)
		c.Cloudflared = strings.TrimSpace(cloudflared)
		if mode == "relay" {
			c.RelayURL = base
			if key != "" {
				c.EnrollmentKey = key
			}
		}
		return nil
	})
}
