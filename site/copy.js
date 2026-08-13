// A copy button on every code block.
//
// Two things make a snippet on a page annoying to use: retyping it, and the
// prompt. The prompt is handled in CSS — `$ ` is a ::before pseudo-element, so
// it is drawn but is not text, and therefore is not selected by a triple click,
// not copied, and not pasted into a shell where it would run nothing. This adds
// the other half.
//
// What it copies is deliberate. A block that mixes commands with the output
// they produce copies only the commands, because the output is there to be
// recognised rather than run. A block with no commands in it — a config file, a
// status listing — copies whole, because then the whole thing is the point.

(function () {
    const blocks = document.querySelectorAll("pre");
    if (!blocks.length || !navigator.clipboard) return;

    for (const pre of blocks) {
        const button = document.createElement("button");
        button.className = "copy";
        button.type = "button";
        button.textContent = "copy";
        // The block is what a reader is looking at; the button should not be
        // announced before it.
        button.setAttribute("aria-label", "Copy this snippet");

        button.addEventListener("click", async () => {
            const commands = pre.querySelectorAll(".cmd");
            const text = commands.length
                ? Array.from(commands, (c) => c.textContent.trim()).join("\n")
                : pre.textContent.replace(/\n+$/, "");

            try {
                await navigator.clipboard.writeText(text);
                button.textContent = commands.length > 1
                    ? `copied ${commands.length} lines`
                    : "copied";
            } catch (e) {
                // Clipboard access can be refused — an insecure origin, or a
                // permission the reader declined. Saying so beats a button
                // that silently does nothing.
                button.textContent = "select it instead";
            }
            setTimeout(() => { button.textContent = "copy"; }, 1600);
        });

        pre.appendChild(button);
    }
})();
