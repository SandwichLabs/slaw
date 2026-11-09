// Where a page's components live, for their runs' receipts. A notebook's A2UI
// cell provides {notebook, cell}; an artifact view could provide {artifact}. A
// component's live query then says which component asked (RenderSource), and Go
// checks it against the open notebook or artifact and drops what does not match.
// A page with no source (a chat reply) provides nothing, and its runs are plain
// renders.
import { inject, type InjectionKey } from 'vue'

export interface A2UISource {
  notebook?: string
  artifact?: string
  cell?: string
}

export const A2UI_SOURCE: InjectionKey<A2UISource | undefined> = Symbol('a2uiSource')

/** A function from a component id to the RenderSource of its runs; undefined when the page has no source. */
export function useRenderSource(): (componentId: string) => RenderSource | undefined {
  const src = inject(A2UI_SOURCE, undefined)
  return (component) => (src ? { kind: 'a2ui', ...src, component } : undefined)
}
