package chat

import (
	"testing"

	"projectos/kernel/internal/modelcfg"
)

func TestApprovalModelVariantIsolation(t *testing.T) {
	settings := modelcfg.Settings{Variants: map[string]string{"approval": "low", "chat": "high"}}
	// No approval model: the chat session model keeps its own variant and the
	// approval variant is not attached to it.
	if got := approvalModel(settings, "provider/chat#max", "provider/default", "high"); got != "provider/chat#max" {
		t.Fatalf("chat fallback = %q", got)
	}
	// Production stores the base chat model and applies the variant only when
	// creating the chat session; the judge must re-apply that chat variant.
	if got := approvalModel(settings, "provider/chat", "provider/default", "high"); got != "provider/chat#high" {
		t.Fatalf("base chat fallback = %q", got)
	}
	// Explicit approval role: its own model and variant.
	settings.Approval = "provider/fast"
	if got := approvalModel(settings, "provider/chat#max", "provider/default", "high"); got != "provider/fast#low" {
		t.Fatalf("approval role = %q", got)
	}
	// No session model: the built-in model follows the chat variant.
	settings.Approval = ""
	if got := approvalModel(settings, "", "provider/default", "high"); got != "provider/default#high" {
		t.Fatalf("default fallback = %q", got)
	}
}

func TestLiveChatVariantReadsRuntimeSettings(t *testing.T) {
	root := t.TempDir()
	m := &Manager{cfg: Config{Root: root, Variant: "low"}}
	if got := m.liveChatVariant(); got != "low" {
		t.Fatalf("startup fallback = %q", got)
	}
	if err := modelcfg.Save(root, modelcfg.Settings{Chat: "provider/model",
		Variants: map[string]string{"chat": "high"}}); err != nil {
		t.Fatal(err)
	}
	if got := m.liveChatVariant(); got != "high" {
		t.Fatalf("runtime variant = %q", got)
	}
	if err := modelcfg.Save(root, modelcfg.Settings{Chat: "provider/model"}); err != nil {
		t.Fatal(err)
	}
	if got := m.liveChatVariant(); got != "" {
		t.Fatalf("cleared runtime variant = %q", got)
	}
}
