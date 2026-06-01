// Type declaration for ansi_up v6.x (vendored at build time).
// Only the subset we use is declared here.
declare module "ansi_up" {
  export class AnsiUp {
    set use_classes(value: boolean);
    get use_classes(): boolean;
    set escape_html(value: boolean);
    get escape_html(): boolean;
    ansi_to_html(text: string): string;
  }
}
