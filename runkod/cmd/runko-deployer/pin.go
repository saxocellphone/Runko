package main

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/saxocellphone/runko/platform/checks"
)

// pinImages rewrites kustomization.yaml's `images:` transformer so each
// reported image is pinned to its digest (name: <image_ref>, digest: <digest>),
// which kustomize applies wherever a manifest references that image. It edits
// the YAML surgically via yaml.Node, so `resources`/`generators`, key order,
// and comments all survive - a minimal, reviewable GitOps diff. Returns whether
// the file's bytes changed (false => digests already pinned, no commit).
func pinImages(path string, images []checks.DeployImage) (bool, error) {
	before, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(before, &doc); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return false, fmt.Errorf("%s: not a kustomization mapping", path)
	}
	root := doc.Content[0]
	seq := ensureSeq(root, "images")
	for _, img := range images {
		if img.ImageRef == "" || img.Digest == "" {
			continue // nothing to pin without a full ref + digest
		}
		setImageDigest(seq, img.ImageRef, img.Digest)
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return false, fmt.Errorf("encode %s: %w", path, err)
	}
	enc.Close()
	if bytes.Equal(before, out.Bytes()) {
		return false, nil
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// findValue returns the value node for key in a mapping node, or nil.
func findValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// ensureSeq returns the sequence node under key, creating an empty one if
// absent (or coercing a wrong-typed value to a fresh sequence).
func ensureSeq(mapping *yaml.Node, key string) *yaml.Node {
	if v := findValue(mapping, key); v != nil {
		if v.Kind != yaml.SequenceNode {
			v.Kind, v.Tag, v.Content = yaml.SequenceNode, "!!seq", nil
		}
		return v
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	mapping.Content = append(mapping.Content, scalar(key), seq)
	return seq
}

// setImageDigest sets (or adds) the digest pin for image `name` in the images
// sequence. A digest supersedes any newTag on an existing entry.
func setImageDigest(seq *yaml.Node, name, digest string) {
	for _, item := range seq.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		if n := findValue(item, "name"); n != nil && n.Value == name {
			setScalar(item, "digest", digest)
			removeKey(item, "newTag")
			return
		}
	}
	entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	entry.Content = append(entry.Content, scalar("name"), scalar(name), scalar("digest"), scalar(digest))
	seq.Content = append(seq.Content, entry)
}

func setScalar(mapping *yaml.Node, key, value string) {
	if v := findValue(mapping, key); v != nil {
		v.Kind, v.Tag, v.Value = yaml.ScalarNode, "!!str", value
		return
	}
	mapping.Content = append(mapping.Content, scalar(key), scalar(value))
}

func removeKey(mapping *yaml.Node, key string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

func scalar(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}
