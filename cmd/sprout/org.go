package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/adityaraj/sprout/internal/cliconfig"
	"github.com/adityaraj/sprout/internal/meta"
)

func runOrg(c *client, args []string) error {
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "list":
		var out struct {
			Orgs       []meta.Org `json:"orgs"`
			CurrentOrg string     `json:"current_org"`
			CurrentID  string     `json:"current_id"`
		}
		if err := c.do("GET", "/v1/orgs", nil, &out); err != nil {
			return err
		}
		if len(out.Orgs) == 0 {
			fmt.Println("(no orgs)")
			return nil
		}
		cur := c.org
		if out.CurrentOrg != "" {
			cur = out.CurrentOrg
		}
		for _, o := range out.Orgs {
			mark := " "
			if o.Name == cur || o.ID == cur || o.ID == out.CurrentID {
				mark = "*"
			}
			role := o.Role
			if role == "" {
				role = "-"
			}
			fmt.Printf("%s %-16s %-8s owner=%-16s %s\n", mark, o.Name, role, o.CreatedBy, o.ID)
		}
		return nil
	case "create":
		if len(args) < 2 {
			return fmt.Errorf("usage: sprout org create <name>")
		}
		var org meta.Org
		if err := c.do("POST", "/v1/orgs", map[string]string{"name": args[1]}, &org); err != nil {
			return err
		}
		fmt.Printf("✓ created org %s (%s)\n", org.Name, org.ID)
		return nil
	case "use":
		if len(args) < 2 {
			return fmt.Errorf("usage: sprout org use <name-or-id>")
		}
		name := strings.TrimSpace(args[1])
		saved, err := cliconfig.Save(cliconfig.File{Org: name})
		if err != nil {
			return err
		}
		fmt.Printf("✓ current org %s (%s)\n", saved.Org, cliconfig.Path())
		return nil
	case "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: sprout org delete <name-or-id>")
		}
		if err := c.do("DELETE", "/v1/orgs/"+url.PathEscape(args[1]), nil, nil); err != nil {
			return err
		}
		fmt.Printf("✓ deleted org %s\n", args[1])
		return nil
	case "members":
		return runOrgMembers(c, args[1:])
	default:
		usage()
		os.Exit(2)
	}
	return nil
}

func runOrgMembers(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sprout org members list|add|remove ...")
	}
	orgName := c.org
	if orgName == "" {
		orgName = "default"
	}
	switch args[0] {
	case "list":
		if len(args) >= 2 && !strings.HasPrefix(args[1], "-") {
			orgName = args[1]
		}
		var list []meta.OrgMember
		if err := c.do("GET", "/v1/orgs/"+url.PathEscape(orgName)+"/members", nil, &list); err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("(no members)")
			return nil
		}
		for _, m := range list {
			fmt.Printf("%-16s %-8s added_by=%s\n", m.Login, m.Role, m.AddedBy)
		}
		return nil
	case "add":
		if len(args) < 2 {
			return fmt.Errorf("usage: sprout org members add <github-login>")
		}
		var m meta.OrgMember
		if err := c.do("POST", "/v1/orgs/"+url.PathEscape(orgName)+"/members", map[string]string{"login": args[1]}, &m); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(m, "", "  ")
		fmt.Printf("✓ added %s as %s\n%s\n", m.Login, m.Role, string(b))
		return nil
	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: sprout org members remove <github-login>")
		}
		if err := c.do("DELETE", "/v1/orgs/"+url.PathEscape(orgName)+"/members/"+url.PathEscape(args[1]), nil, nil); err != nil {
			return err
		}
		fmt.Printf("✓ removed %s from %s\n", args[1], orgName)
		return nil
	default:
		return fmt.Errorf("usage: sprout org members list|add|remove ...")
	}
}
