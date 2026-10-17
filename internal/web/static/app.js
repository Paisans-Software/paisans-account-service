// Every page works without this file; it only adds conveniences.
"use strict";

// The accent colour replaces --primary, so its text colour is chosen for
// contrast, as Pocket ID does in frontend/src/lib/utils/accent-color-util.ts.
function luminance(color) {
	const probe = document.createElement("div");
	probe.style.color = color;
	document.body.appendChild(probe);
	const rgb = getComputedStyle(probe).color.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)/);
	probe.remove();
	if (!rgb) return 0.5;
	const lin = (v) => (v /= 255) <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
	return 0.2126 * lin(+rgb[1]) + 0.7152 * lin(+rgb[2]) + 0.0722 * lin(+rgb[3]);
}

function accentForeground() {
	const root = document.documentElement;
	if (!root.hasAttribute("data-accent")) return;
	const primary = getComputedStyle(root).getPropertyValue("--primary").trim();
	root.style.setProperty("--primary-foreground", luminance(primary) < 0.55 ? "oklch(0.98 0 0)" : "oklch(0.09 0 0)");
}

// A button with data-copy="id" copies that element's text. It is hidden in
// the markup, so without this script there is no button that does nothing.
function copyButtons() {
	for (const button of document.querySelectorAll("button[data-copy]")) {
		const source = document.getElementById(button.dataset.copy);
		if (!source) continue;
		const label = button.textContent;
		button.hidden = false;
		button.addEventListener("click", async () => {
			try {
				await navigator.clipboard.writeText(source.textContent.trim());
				button.textContent = "Copied";
			} catch {
				// No clipboard, as over plain HTTP: select the text to copy by hand.
				getSelection().selectAllChildren(source);
				button.textContent = "Press Ctrl+C or ⌘C";
			}
			setTimeout(() => { button.textContent = label; }, 2000);
		});
	}
}

// An input with data-lowercase shows what the server will store.
function lowercaseInputs() {
	for (const input of document.querySelectorAll("input[data-lowercase]")) {
		input.addEventListener("input", () => {
			const lower = input.value.toLowerCase();
			if (lower === input.value) return;
			const { selectionStart: start, selectionEnd: end } = input;
			input.value = lower;
			input.setSelectionRange(start, end);
		});
	}
}

accentForeground();
copyButtons();
lowercaseInputs();
