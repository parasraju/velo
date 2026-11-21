package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// UpdateProvider writes the provider selection into the config file. The API
// key itself is never stored; only the environment variable name is.
func UpdateProvider(path string, p ProviderConfig) error {
	if path == "" {
		return fmt.Errorf("config path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	var doc yaml.Node
	data, readErr := os.ReadFile(abs)
	if readErr == nil && len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("decode config %s: %w", abs, err)
		}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("config %s root must be a mapping", abs)
	}
	prov := ensureMap(root, "provider")
	setScalar(prov, "name", p.Name)
	setScalar(prov, "model", p.Model)
	setScalar(prov, "base_url", p.BaseURL)
	setScalar(prov, "api_key_env", p.APIKeyEnv)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		_ = enc.Close()
		return fmt.Errorf("encode config %s: %w", abs, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode config %s: %w", abs, err)
	}
	if err := writeFileAtomic(abs, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", abs, err)
	}
	return nil
}

func ensureMap(parent *yaml.Node, key string) *yaml.Node {
	if child := mapChild(parent, key); child != nil {
		if child.Kind != yaml.MappingNode {
			child.Kind = yaml.MappingNode
			child.Tag = "!!map"
			child.Content = nil
		}
		return child
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, k, v)
	return v
}

func mapChild(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			return parent.Content[i+1]
		}
	}
	return nil
}

func setScalar(parent *yaml.Node, key, value string) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content[i+1].Kind = yaml.ScalarNode
			parent.Content[i+1].Tag = "!!str"
			parent.Content[i+1].Value = value
			return
		}
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}
