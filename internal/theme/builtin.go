package theme

// builtin are the themes that come with canopy, besides "default". Each
// sets every colour, so it looks the same whichever mode it is used for.
// Colours follow each theme's published palette; the activity graph runs
// from the background to the theme's accent.
var builtin = map[string]Palette{
	"github-light": {Highlight: "github", Colors: map[string]string{
		"bg": "#ffffff", "fg": "#1f2328", "muted": "#59636e",
		"link": "#0969da", "link-hover": "#0550ae", "border": "#d1d9e0",
		"header-bg": "#f6f8fa", "event-bg": "#f6f8fa", "code-bg": "#f6f8fa",
		"btn-bg": "#f6f8fa", "btn-hover": "#eff2f5", "target": "#fff8c5",
		"add": "#1a7f37", "add-bg": "#dafbe1", "del": "#d1242f", "del-bg": "#ffebe9",
		"hunk": "#8250df", "branch-bg": "#ddf4ff", "tag-bg": "#dafbe1",
		"act-0": "#eff2f5", "act-1": "#aceebb", "act-2": "#4ac26b", "act-3": "#2da44e", "act-4": "#116329",
	}},
	"github-dark": {Highlight: "github-dark", Colors: map[string]string{
		"bg": "#0d1117", "fg": "#e6edf3", "muted": "#9198a1",
		"link": "#4493f8", "link-hover": "#79c0ff", "border": "#3d444d",
		"header-bg": "#151b23", "event-bg": "#151b23", "code-bg": "#151b23",
		"btn-bg": "#212830", "btn-hover": "#262c36", "target": "#3b2e0c",
		"add": "#3fb950", "add-bg": "#12261e", "del": "#f85149", "del-bg": "#25171c",
		"hunk": "#a371f7", "branch-bg": "#121d2f", "tag-bg": "#12261e",
		"act-0": "#151b23", "act-1": "#033a16", "act-2": "#196c2e", "act-3": "#2ea043", "act-4": "#56d364",
	}},
	"ayu-light": {Highlight: "ayu-light", Colors: map[string]string{
		"bg": "#fcfcfc", "fg": "#5c6166", "muted": "#8a9199",
		"link": "#399ee6", "link-hover": "#2a7cba", "border": "#e0e3e6",
		"header-bg": "#f3f4f5", "event-bg": "#f3f4f5", "code-bg": "#f8f9fa",
		"btn-bg": "#eef0f2", "btn-hover": "#e4e7ea", "target": "#ffeacc",
		"add": "#6cbf43", "add-bg": "#ebf6e4", "del": "#e65050", "del-bg": "#fdeaea",
		"hunk": "#a37acc", "branch-bg": "#e1f0fb", "tag-bg": "#eef6dc",
		"act-0": "#eef0f2", "act-1": "#ffe0b2", "act-2": "#ffc670", "act-3": "#ffaa33", "act-4": "#e08a00",
	}},
	"ayu-mirage": {Highlight: "ayu-mirage", Colors: map[string]string{
		"bg": "#242936", "fg": "#cccac2", "muted": "#8a9199",
		"link": "#73d0ff", "link-hover": "#a6e1ff", "border": "#3a4152",
		"header-bg": "#1f2430", "event-bg": "#2a303c", "code-bg": "#1f2430",
		"btn-bg": "#2d3442", "btn-hover": "#363e4e", "target": "#4b4532",
		"add": "#87d96c", "add-bg": "#2b3a2e", "del": "#f27983", "del-bg": "#3d2a33",
		"hunk": "#dfbfff", "branch-bg": "#233a50", "tag-bg": "#2c3d2a",
		"act-0": "#2d3442", "act-1": "#5c4a2a", "act-2": "#8f6f30", "act-3": "#c99a3c", "act-4": "#ffcc66",
	}},
	"light-owl": {Highlight: "light-owl", Colors: map[string]string{
		"bg": "#fbfbfb", "fg": "#403f53", "muted": "#6f7f8a",
		"link": "#4876d6", "link-hover": "#2a5cb8", "border": "#d9d9d9",
		"header-bg": "#f0f0f0", "event-bg": "#f3f3f3", "code-bg": "#f6f6f6",
		"btn-bg": "#ececec", "btn-hover": "#e0e0e0", "target": "#fbe9a8",
		"add": "#08916a", "add-bg": "#e1f3ec", "del": "#de3d3b", "del-bg": "#fbe6e6",
		"hunk": "#994cc3", "branch-bg": "#e3ebfa", "tag-bg": "#e1f3ec",
		"act-0": "#ececec", "act-1": "#b6e3d4", "act-2": "#6cc3a6", "act-3": "#2aa298", "act-4": "#08916a",
	}},
	"night-owl": {Highlight: "night-owl", Colors: map[string]string{
		"bg": "#011627", "fg": "#d6deeb", "muted": "#8a9fb3",
		"link": "#82aaff", "link-hover": "#c5e4fd", "border": "#1d3b53",
		"header-bg": "#0b2942", "event-bg": "#0b253a", "code-bg": "#01111d",
		"btn-bg": "#0b2942", "btn-hover": "#1d3b53", "target": "#3d3a1f",
		"add": "#addb67", "add-bg": "#13301f", "del": "#ef5350", "del-bg": "#3a1420",
		"hunk": "#c792ea", "branch-bg": "#13344f", "tag-bg": "#183a2a",
		"act-0": "#0b2942", "act-1": "#14503a", "act-2": "#1f7a4a", "act-3": "#2cae5c", "act-4": "#addb67",
	}},
	"catppuccin-latte": {Highlight: "catppuccin-latte", Colors: map[string]string{
		"bg": "#eff1f5", "fg": "#4c4f69", "muted": "#6c6f85",
		"link": "#1e66f5", "link-hover": "#7287fd", "border": "#ccd0da",
		"header-bg": "#e6e9ef", "event-bg": "#e6e9ef", "code-bg": "#e6e9ef",
		"btn-bg": "#dce0e8", "btn-hover": "#ccd0da", "target": "#f6e3c4",
		"add": "#40a02b", "add-bg": "#dcebd8", "del": "#d20f39", "del-bg": "#f4d6dc",
		"hunk": "#8839ef", "branch-bg": "#d5e0f8", "tag-bg": "#dcebd8",
		"act-0": "#dce0e8", "act-1": "#b7dca9", "act-2": "#84c26f", "act-3": "#5bab43", "act-4": "#40a02b",
	}},
	"catppuccin-frappe": {Highlight: "catppuccin-frappe", Colors: map[string]string{
		"bg": "#303446", "fg": "#c6d0f5", "muted": "#a5adce",
		"link": "#8caaee", "link-hover": "#babbf1", "border": "#51576d",
		"header-bg": "#292c3c", "event-bg": "#292c3c", "code-bg": "#292c3c",
		"btn-bg": "#414559", "btn-hover": "#51576d", "target": "#5a5240",
		"add": "#a6d189", "add-bg": "#3b4a3c", "del": "#e78284", "del-bg": "#4d3a44",
		"hunk": "#ca9ee6", "branch-bg": "#3c4a6b", "tag-bg": "#3b4a3c",
		"act-0": "#414559", "act-1": "#4f6a4a", "act-2": "#6c9258", "act-3": "#8bb56f", "act-4": "#a6d189",
	}},
}
