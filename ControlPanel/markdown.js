'use strict';
// Small, local, DOM-only Markdown renderer. It never assigns innerHTML.
// Supported: headings, paragraphs, ordered/unordered lists, emphasis, inline code, HTTPS links, rules.
(function () {
  function inline(text, parent) {
    const token = /(`[^`]+`|\*\*[^*]+\*\*|\*[^*]+\*|\[[^\]]+\]\(https:\/\/[^)]+\))/g;
    let offset = 0, match;
    while ((match = token.exec(text))) {
      parent.append(document.createTextNode(text.slice(offset, match.index)));
      const value = match[0];
      let node;
      if (value[0] === '`') { node = document.createElement('code'); node.textContent = value.slice(1, -1); }
      else if (value.startsWith('**')) { node = document.createElement('strong'); node.textContent = value.slice(2, -2); }
      else if (value.startsWith('*')) { node = document.createElement('em'); node.textContent = value.slice(1, -1); }
      else {
        const parts = /^\[([^\]]+)\]\((https:\/\/[^)]+)\)$/.exec(value);
        node = document.createElement('a'); node.textContent = parts[1]; node.href = parts[2];
        node.target = '_blank'; node.rel = 'noopener noreferrer';
      }
      parent.append(node); offset = token.lastIndex;
    }
    parent.append(document.createTextNode(text.slice(offset)));
  }
  window.renderMarkdownSafe = function (source, target) {
    target.replaceChildren();
    let list = null;
    for (const line of String(source).split(/\r?\n/)) {
      if (!line.trim()) { list = null; continue; }
      const heading = /^(#{1,3})\s+(.*)$/.exec(line);
      if (heading) { list = null; const el = document.createElement('h' + heading[1].length); inline(heading[2], el); target.append(el); continue; }
      if (/^\s*---+\s*$/.test(line)) { list = null; target.append(document.createElement('hr')); continue; }
      const unordered = /^\s*[-*]\s+(.*)$/.exec(line);
      const ordered = /^\s*\d+\.\s+(.*)$/.exec(line);
      if (unordered || ordered) {
        const type = ordered ? 'OL' : 'UL';
        if (!list || list.tagName !== type) { list = document.createElement(type.toLowerCase()); target.append(list); }
        const li = document.createElement('li'); inline((unordered || ordered)[1], li); list.append(li); continue;
      }
      list = null; const paragraph = document.createElement('p'); inline(line, paragraph); target.append(paragraph);
    }
  };
})();
