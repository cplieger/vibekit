// Test stub for ansi_up. In tests we don't need real ANSI parsing —
// just pass text through with HTML escaping (matching escape_html=true).
export class AnsiUp {
  use_classes = false;
  escape_html = true;
  ansi_to_html(text: string): string {
    // Strip ANSI codes and escape HTML (minimal stub).
    const stripped = text.replace(/\x1b\[[0-9;]*m/g, "");
    return stripped
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;");
  }
}
