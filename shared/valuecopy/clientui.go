package valuecopy

import "core/shared/clientui"

func ToolCallMeta(meta *clientui.ToolCallMeta) *clientui.ToolCallMeta {
	if meta == nil {
		return nil
	}
	copyMeta := *meta
	if len(meta.Suggestions) > 0 {
		copyMeta.Suggestions = append([]string(nil), meta.Suggestions...)
	}
	if meta.RenderHint != nil {
		renderHint := *meta.RenderHint
		copyMeta.RenderHint = &renderHint
	}
	copyMeta.ShellExitCode = Pointer(meta.ShellExitCode)
	copyMeta.PatchRender = RenderedPatch(meta.PatchRender)
	return &copyMeta
}
