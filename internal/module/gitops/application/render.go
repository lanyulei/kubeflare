package application

import (
	"fmt"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// 渲染方式取值。与 dto 校验 oneof=kustomize helm raw 保持一致,集中定义避免散落字面量。
const (
	RENDER_TYPE_KUSTOMIZE = "kustomize"
	RENDER_TYPE_HELM      = "helm"
	RENDER_TYPE_RAW       = "raw"
)

// ManifestTarget 描述一次渲染要落地的"目标镜像":把 manifest 中对应镜像的引用更新到
// Digest(形如 sha256:...)。Repo 为镜像仓库地址(可空),用于在 kustomize images /
// helm values 中按名匹配应被更新的条目。
type ManifestTarget struct {
	Repo   string
	Digest string
}

// RenderedFile 是一次渲染产出的单个文件变更:Path 为仓库内相对路径,Content 为新全文。
// actuator 据此向 Git 提交(更新已存在文件)。
type RenderedFile struct {
	Path    string
	Content string
}

// ManifestRenderer 把"发布单要求的目标镜像"渲染为对仓库文件的具体修改。provider 无关:
// 输入当前文件内容,输出更新后的内容;真正的"读文件 / 提交 commit"由 infrastructure 完成。
// 不同 render_type(kustomize/helm/raw)用不同策略定位并改写镜像引用。
type ManifestRenderer interface {
	// Render 计算把 target 应用到 path 处当前内容 current 后的新内容。无需改动时返回
	// changed=false(actuator 据此跳过空提交)。path 仅用于错误信息与日志。
	Render(path string, current string, target ManifestTarget) (content string, changed bool, err error)
}

// ManifestFilePath 由 render_type 与应用/环境路径推导出待改写的 manifest 文件相对路径。
//   - kustomize:overlay 目录下的 kustomization.yaml(overlay 为空时退回 manifest 根);
//   - helm:overlay 指向的 values 文件(为空时退回 manifest 根下 values.yaml);
//   - raw:overlay 指向的单个 manifest 文件(为空时退回 manifest 根)。
//
// overlay 以 "/" 结尾或无扩展名时视为目录,补出对应默认文件名。
func ManifestFilePath(renderType string, manifestPath string, overlayPath string) string {
	base := joinRepoPath(strings.TrimSpace(manifestPath), strings.TrimSpace(overlayPath))
	switch strings.TrimSpace(renderType) {
	case RENDER_TYPE_KUSTOMIZE:
		return ensureFileName(base, "kustomization.yaml")
	case RENDER_TYPE_HELM:
		return ensureFileName(base, "values.yaml")
	default: // raw
		return ensureFileName(base, "deployment.yaml")
	}
}

// joinRepoPath 以仓库根为基准拼接 manifest 根与 overlay 子路径,清理多余分隔符。overlay
// 为绝对路径(以 / 开头)时按相对仓库根处理(去掉前导 /),避免拼出越界路径。
func joinRepoPath(base string, overlay string) string {
	base = strings.Trim(base, "/")
	overlay = strings.Trim(overlay, "/")
	switch {
	case base == "" && overlay == "":
		return ""
	case base == "":
		return overlay
	case overlay == "":
		return base
	default:
		return base + "/" + overlay
	}
}

// ensureFileName 在 path 看起来是目录(空 / 末段无 . 扩展名)时补上默认文件名。
func ensureFileName(path string, defaultName string) string {
	path = strings.Trim(path, "/")
	if path == "" {
		return defaultName
	}
	last := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		last = path[idx+1:]
	}
	if strings.Contains(last, ".") {
		return path
	}
	return path + "/" + defaultName
}

// DefaultManifestRenderer 是内置渲染器,覆盖 kustomize/helm/raw 三类:
//   - kustomize:更新 images 列表中匹配 repo 的条目的 digest(无匹配时按首条或追加);
//   - helm:更新 values 中 image.tag/image.digest(或 image 字符串);
//   - raw:正文里 "image: repo[@digest|:tag]" 的镜像引用按 repo 匹配后改写 digest。
//
// 设计为最小可用实现:以 sha256 digest 形式落地(image@sha256:...),保证可溯源;复杂
// 多镜像 / 嵌套结构的精确改写可在此基础上扩展。
type DefaultManifestRenderer struct{}

func (DefaultManifestRenderer) Render(path string, current string, target ManifestTarget) (string, bool, error) {
	digest := strings.TrimSpace(target.Digest)
	if digest == "" {
		// 无目标 digest 时不改动(由调用方决定是否跳过)。
		return current, false, nil
	}
	switch detectRenderTypeByPath(path) {
	case RENDER_TYPE_KUSTOMIZE:
		return renderKustomizeImages(current, target)
	default:
		// helm values / raw manifest 都是 YAML 文档,统一走 image 节点改写。
		return renderYAMLImage(current, target)
	}
}

// detectRenderTypeByPath 据文件名粗判渲染类型,使 Render 无需额外传 render_type。
func detectRenderTypeByPath(path string) string {
	base := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		base = path[idx+1:]
	}
	if strings.EqualFold(base, "kustomization.yaml") || strings.EqualFold(base, "kustomization.yml") {
		return RENDER_TYPE_KUSTOMIZE
	}
	return RENDER_TYPE_RAW
}

// kustomizeImage 是 kustomization.yaml 中 images 列表项的子集(仅取改写所需字段)。
type kustomizeImage struct {
	Name    string `yaml:"name"`
	NewName string `yaml:"newName,omitempty"`
	NewTag  string `yaml:"newTag,omitempty"`
	Digest  string `yaml:"digest,omitempty"`
}

// renderKustomizeimages 反序列化 kustomization 的 images 列表,更新匹配 repo 的条目 digest。
// 无 images 列表时新增一条;有列表但无匹配时优先按 newName 匹配,否则追加新条目。
func renderKustomizeImages(current string, target ManifestTarget) (string, bool, error) {
	var doc map[string]any
	if strings.TrimSpace(current) != "" {
		if err := yaml.Unmarshal([]byte(current), &doc); err != nil {
			return current, false, fmt.Errorf("parse kustomization yaml: %w", err)
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}

	images := parseKustomizeImages(doc["images"])
	repo := strings.TrimSpace(target.Repo)
	digest := strings.TrimSpace(target.Digest)

	matched := false
	for i := range images {
		// repo 为空时匹配首条;否则按 name/newName 命中。
		if repo == "" || images[i].Name == repo || images[i].NewName == repo {
			images[i].Digest = digest
			images[i].NewTag = "" // digest 与 tag 互斥,置空避免二义。
			matched = true
			if repo == "" {
				break
			}
		}
	}
	if !matched {
		name := repo
		if name == "" {
			name = "app"
		}
		images = append(images, kustomizeImage{Name: name, Digest: digest})
	}

	// 回写 images 列表为通用 map 切片,保留 yaml 其余键不动。
	out := make([]map[string]any, 0, len(images))
	for _, img := range images {
		entry := map[string]any{"name": img.Name}
		if img.NewName != "" {
			entry["newName"] = img.NewName
		}
		if img.Digest != "" {
			entry["digest"] = img.Digest
		}
		if img.NewTag != "" {
			entry["newTag"] = img.NewTag
		}
		out = append(out, entry)
	}
	doc["images"] = out

	encoded, err := marshalYAML(doc)
	if err != nil {
		return current, false, err
	}
	if encoded == current {
		return current, false, nil
	}
	return encoded, true, nil
}

// parseKustomizeImages 把 doc["images"] 宽松解析为 kustomizeImage 列表(容忍缺失/类型不符)。
func parseKustomizeImages(raw any) []kustomizeImage {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	images := make([]kustomizeImage, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		images = append(images, kustomizeImage{
			Name:    stringField(m, "name"),
			NewName: stringField(m, "newName"),
			NewTag:  stringField(m, "newTag"),
			Digest:  stringField(m, "digest"),
		})
	}
	return images
}

// renderYAMLImage 在 helm values / raw manifest 文档中,把 image 引用更新到目标 digest。
// 支持两种常见形态:
//   - 顶层/嵌套的 image 字符串:"image: repo:tag" → "image: repo@sha256:..."
//   - image 映射:{repository: repo, tag: x} → 增设/更新 digest 键。
func renderYAMLImage(current string, target ManifestTarget) (string, bool, error) {
	if strings.TrimSpace(current) == "" {
		// 空文件无从定位 image 节点,生成一个最小 image 字段,保证 digest 可落地。
		content := "image: " + composeImageRef(target.Repo, target.Digest) + "\n"
		return content, true, nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(current), &root); err != nil {
		return current, false, fmt.Errorf("parse yaml: %w", err)
	}
	changed := updateImageNodes(&root, target)
	if !changed {
		return current, false, nil
	}
	encoded, err := marshalNode(&root)
	if err != nil {
		return current, false, err
	}
	return encoded, true, nil
}

// updateImageNodes 递归遍历 YAML 节点树,改写所有 image 标量与 image 映射的 digest/tag。
// 返回是否发生改动。
func updateImageNodes(node *yaml.Node, target ManifestTarget) bool {
	if node == nil {
		return false
	}
	changed := false
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			if updateImageNodes(child, target) {
				changed = true
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if updateImageNodes(child, target) {
				changed = true
			}
		}
	case yaml.MappingNode:
		// MappingNode.Content 是 [key0,val0,key1,val1,...]。
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			val := node.Content[i+1]
			if strings.EqualFold(key.Value, "image") {
				if updateSingleImage(val, target) {
					changed = true
				}
				continue
			}
			if updateImageNodes(val, target) {
				changed = true
			}
		}
	}
	return changed
}

// updateSingleimage 改写单个 image 值节点:标量直接替换为 repo@digest;映射则更新
// tag/digest 子键。repo 非空且与现值不匹配时跳过(避免误改无关镜像)。
func updateSingleImage(val *yaml.Node, target ManifestTarget) bool {
	repo := strings.TrimSpace(target.Repo)
	digest := strings.TrimSpace(target.Digest)
	switch val.Kind {
	case yaml.ScalarNode:
		existingRepo := repoOfImageRef(val.Value)
		if repo != "" && existingRepo != "" && existingRepo != repo {
			return false
		}
		useRepo := repo
		if useRepo == "" {
			useRepo = existingRepo
		}
		newRef := composeImageRef(useRepo, digest)
		if newRef == val.Value {
			return false
		}
		val.Value = newRef
		val.Tag = "!!str"
		return true
	case yaml.MappingNode:
		return updateImageMapping(val, target)
	}
	return false
}

// updateImageMapping 更新 {repository,tag,digest} 形态的 image 映射:设 digest、清空 tag。
func updateImageMapping(val *yaml.Node, target ManifestTarget) bool {
	digest := strings.TrimSpace(target.Digest)
	var repoNode, tagNode, digestNode *yaml.Node
	for i := 0; i+1 < len(val.Content); i += 2 {
		key := val.Content[i].Value
		switch {
		case strings.EqualFold(key, "repository"):
			repoNode = val.Content[i+1]
		case strings.EqualFold(key, "tag"):
			tagNode = val.Content[i+1]
		case strings.EqualFold(key, "digest"):
			digestNode = val.Content[i+1]
		}
	}
	repo := strings.TrimSpace(target.Repo)
	if repo != "" && repoNode != nil && strings.TrimSpace(repoNode.Value) != "" && repoNode.Value != repo {
		return false
	}
	changed := false
	if digestNode != nil {
		if digestNode.Value != digest {
			digestNode.Value = digest
			digestNode.Tag = "!!str"
			changed = true
		}
	} else {
		val.Content = append(val.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "digest", Tag: "!!str"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: digest, Tag: "!!str"},
		)
		changed = true
	}
	// digest 落地后清空 tag,避免 tag 与 digest 同时存在导致歧义。
	if tagNode != nil && strings.TrimSpace(tagNode.Value) != "" {
		tagNode.Value = ""
		changed = true
	}
	return changed
}

// composeImageRef 由 repo 与 digest 拼出 "repo@sha256:..." 引用;repo 为空时仅返回 digest。
func composeImageRef(repo string, digest string) string {
	repo = strings.TrimSpace(repo)
	digest = strings.TrimSpace(digest)
	if repo == "" {
		return digest
	}
	return repo + "@" + digest
}

// repoOfImageRef 从 "repo:tag" / "repo@sha256:..." 中提取镜像仓库部分(去掉 tag/digest)。
func repoOfImageRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if idx := strings.Index(ref, "@"); idx >= 0 {
		return ref[:idx]
	}
	// 端口号(host:5000/repo)里的冒号不应被当作 tag 分隔:仅在最后一段含冒号时切分。
	slash := strings.LastIndex(ref, "/")
	lastSegment := ref
	prefix := ""
	if slash >= 0 {
		prefix = ref[:slash+1]
		lastSegment = ref[slash+1:]
	}
	if idx := strings.Index(lastSegment, ":"); idx >= 0 {
		return prefix + lastSegment[:idx]
	}
	return ref
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// marshalYAML 以 2 空格缩进序列化 map 文档,产出稳定可读的 YAML。
func marshalYAML(doc any) (string, error) {
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		_ = enc.Close()
		return "", fmt.Errorf("encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("encode yaml: %w", err)
	}
	return sb.String(), nil
}

// marshalNode 序列化 yaml.Node 树,尽量保留原文档结构与注释。
func marshalNode(node *yaml.Node) (string, error) {
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		_ = enc.Close()
		return "", fmt.Errorf("encode yaml node: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("encode yaml node: %w", err)
	}
	return sb.String(), nil
}
