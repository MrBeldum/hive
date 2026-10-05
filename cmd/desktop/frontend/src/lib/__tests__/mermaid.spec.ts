import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderMermaid, type MermaidPalette } from '../mermaid'

const mermaid = vi.hoisted(() => ({
  initialize: vi.fn(),
  render: vi.fn().mockResolvedValue({ svg: '<svg />' }),
}))
vi.mock('mermaid', () => ({ default: mermaid }))

const palette: MermaidPalette = {
  background: '#101318',
  surface: '#1b2029',
  surfaceAlt: '#232a36',
  border: '#414d5e',
  text: '#e9edf4',
  mutedText: '#a6b0c0',
  accent: '#f5b23f',
  darkMode: true,
  fontFamily: 'Inter, sans-serif',
}

describe('renderMermaid', () => {
  beforeEach(() => {
    mermaid.initialize.mockClear()
    mermaid.render.mockClear().mockResolvedValue({ svg: '<svg />' })
  })

  it('renders with locked-down, app-themed configuration', async () => {
    await expect(renderMermaid('flowchart LR\nA --> B', palette)).resolves.toBe('<svg />')

    expect(mermaid.initialize).toHaveBeenCalledWith(
      expect.objectContaining({
        securityLevel: 'strict',
        secure: expect.arrayContaining([
          'securityLevel',
          'maxTextSize',
          'maxEdges',
          'htmlLabels',
          'dompurifyConfig',
          'theme',
          'themeVariables',
          'themeCSS',
          'fontFamily',
        ]),
        startOnLoad: false,
        htmlLabels: false,
        suppressErrorRendering: true,
        maxTextSize: 50 * 1024,
        maxEdges: 500,
        theme: 'base',
        themeVariables: expect.objectContaining({
          darkMode: true,
          primaryColor: '#1b2029',
          primaryTextColor: '#e9edf4',
          primaryBorderColor: '#414d5e',
          lineColor: '#a6b0c0',
        }),
      }),
    )
    expect(mermaid.render).toHaveBeenCalledWith(expect.stringMatching(/^hive-mermaid-/), 'flowchart LR\nA --> B')
  })
})
