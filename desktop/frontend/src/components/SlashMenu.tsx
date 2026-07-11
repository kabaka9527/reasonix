import { useT, type Translator } from "../lib/i18n";
import type { CommandInfo } from "../lib/types";
import { VirtualMenu } from "./VirtualMenu";

export function slashCommandKindTag(command: CommandInfo, t: Translator): string {
  if (command.plugin) {
    return t("slash.plugin", { name: command.plugin });
  }
  if (command.kind === "custom") return t("slash.project");
  if (command.kind === "mcp") return t("slash.mcp");
  if (command.kind === "skill") return t("slash.skill");
  return "";
}

// SlashMenu is the "/" autocomplete dropdown above the composer. Presentational:
// the Composer owns filtering, the active index, and key handling; this renders
// the (virtualized) list and reports hover/pick. Uses mousedown (not click) so
// picking an item doesn't blur the textarea first.
export function SlashMenu({
  items,
  activeIndex,
  onPick,
  onHover,
}: {
  items: CommandInfo[];
  activeIndex: number;
  onPick: (c: CommandInfo) => void;
  onHover: (i: number) => void;
}) {
  const t = useT();
  return (
    <VirtualMenu
      items={items}
      activeIndex={activeIndex}
      itemKey={(c) => c.kind + ":" + c.name}
      renderItem={(c, i) => (
        <button
          role="option"
          aria-selected={i === activeIndex}
          className={`slashmenu__item ${i === activeIndex ? "slashmenu__item--active" : ""}`}
          onMouseDown={(e) => {
            e.preventDefault();
            onPick(c);
          }}
          onMouseMove={() => onHover(i)}
        >
          <span className="slashmenu__name">/{c.name}</span>
          {c.hint && <span className="slashmenu__hint">{c.hint}</span>}
          <span className="slashmenu__desc">{c.description}</span>
          {slashCommandKindTag(c, t) && <span className="slashmenu__kind">{slashCommandKindTag(c, t)}</span>}
        </button>
      )}
    />
  );
}
