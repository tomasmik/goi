const test = require("node:test");
const assert = require("node:assert/strict");

const dictionaryView = require("../shared/dictionary-view.js");
const subtitleModel = require("../shared/subtitle-model.js");

function fakeDocument() {
  return {
    createElement(tagName) {
      const node = {
        tagName: tagName.toUpperCase(),
        className: "",
        dataset: {},
        children: [],
        textContent: "",
        attributes: {},
        listeners: {},
        classList: {
          toggle(name, enabled) {
            const classes = new Set(node.className.split(/\s+/u).filter(Boolean));
            if (enabled) classes.add(name); else classes.delete(name);
            node.className = Array.from(classes).join(" ");
          },
        },
        setAttribute(name, value) {
          this.attributes[name] = String(value);
        },
        addEventListener(name, listener) {
          this.listeners[name] = listener;
        },
        appendChild(child) {
          child.parentElement = this;
          this.children.push(child);
          return child;
        },
        replaceChildren(...children) {
          this.children = [];
          children.forEach((child) => this.appendChild(child));
        },
      };
      return node;
    },
  };
}

function descendants(node) {
  return node.children.flatMap((child) => [child, ...descendants(child)]);
}

test("renders readings, senses, and commonness for ambiguous results", function () {
  const documentObject = fakeDocument();
  const container = documentObject.createElement("div");
  container.ownerDocument = documentObject;
  const response = {
    ok: true,
    result: {
      query: "生",
      candidates: [{
        written: "生",
        reading: "なま",
        commonness: 10,
        commonness_score: 100,
        meanings: ["raw", "uncooked"],
        senses: [{ parts_of_speech: ["noun"], meanings: ["raw", "uncooked"] }],
      }, {
        written: "生",
        reading: "せい",
        commonness: 4,
        commonness_score: 40,
        meanings: ["life"],
        senses: [{ parts_of_speech: ["noun"], meanings: ["life"] }],
      }],
    },
  };

  dictionaryView.render(container, subtitleModel.dictionaryView(response));

  const nodes = descendants(container);
  assert.equal(container.children[0].textContent, "2 matching entries");
  assert.equal(nodes.filter((node) => node.className === "goi-dictionary-headword").length, 2);
  assert.deepEqual(
    nodes.filter((node) => node.className === "goi-dictionary-reading").map((node) => node.textContent),
    ["なま", "せい"]
  );
  assert.deepEqual(
    nodes.filter((node) => node.tagName === "LI").map((node) => node.textContent),
    ["raw", "uncooked", "life"]
  );
  assert.equal(nodes.some((node) => node.textContent === "Commonness 100/100"), true);
});

test("renders a useful lookup failure", function () {
  const documentObject = fakeDocument();
  const container = documentObject.createElement("div");
  container.ownerDocument = documentObject;

  dictionaryView.render(container, subtitleModel.dictionaryView({ errorCode: "not_connected" }));

  assert.equal(container.children[0].textContent, "Connect Goi to look up words.");
});

test("selects a dictionary entry without saving it", function () {
  const documentObject = fakeDocument();
  const container = documentObject.createElement("div");
  container.ownerDocument = documentObject;
  const selected = [];
  const view = subtitleModel.dictionaryView({
    ok: true,
    result: {
      query: "側",
      candidates: [{
        entry_sequence: 100,
        written: "側",
        reading: "がわ",
        meanings: ["side"],
        senses: [{ meanings: ["side"] }],
      }],
    },
  });

  dictionaryView.render(container, view, {
    selectedEntrySequence: 100,
    onSelect(candidate) { selected.push(candidate.entrySequence); },
  });

  const nodes = descendants(container);
  const action = nodes.find((node) => node.className === "goi-dictionary-select");
  assert.equal(action.textContent, "Selected");
  assert.equal(action.attributes["aria-pressed"], "true");
  action.listeners.click();
  assert.deepEqual(selected, [100]);
});
