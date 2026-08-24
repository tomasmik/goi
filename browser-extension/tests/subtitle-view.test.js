const test = require("node:test");
const assert = require("node:assert/strict");

const subtitleView = require("../shared/subtitle-view.js");

function node(tagName, document) {
  return {
    tagName: tagName.toUpperCase(),
    ownerDocument: document,
    children: [],
    className: "",
    textContent: "",
    attributes: {},
    appendChild(child) {
      this.children.push(child);
      return child;
    },
    setAttribute(name, value) {
      this.attributes[name] = String(value);
    },
  };
}

function fakeDocument() {
  const document = {
    createElement(tagName) {
      return node(tagName, document);
    },
    createTextNode(text) {
      return { nodeType: 3, textContent: text, ownerDocument: document };
    },
  };
  return document;
}

test("renders plain text unless furigana is needed", function () {
  const document = fakeDocument();
  const container = node("span", document);

  subtitleView.renderWord(container, "日本語", { reading: "にほんご", status: "known" }, {
    furiganaEnabled: true,
  });

  assert.equal(container.textContent, "日本語");
  assert.equal(container.children.length, 0);
});

test("renders accessible ruby text for unknown words", function () {
  const document = fakeDocument();
  const container = node("span", document);

  subtitleView.renderWord(container, "日本語", { reading: "にほんご", status: "unknown" }, {
    furiganaEnabled: true,
    rubyClass: "caption-ruby",
  });

  assert.equal(container.children.length, 1);
  assert.equal(container.children[0].tagName, "RUBY");
  assert.equal(container.children[0].className, "caption-ruby");
  assert.equal(container.children[0].children[1].tagName, "RT");
  assert.equal(container.children[0].children[1].textContent, "にほんご");
  assert.equal(container.children[0].children[1].attributes["aria-hidden"], "true");
});
