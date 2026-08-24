function visibleCards(root) {
  return Array.from(root.querySelectorAll("[data-lesson-card]:not([hidden])"));
}

function selectedIDs(form) {
  const selected = new Set(new URL(window.location.href).searchParams.getAll("selected"));
  form.querySelectorAll('[data-lesson-card] input[name="vocabulary_id"]').forEach((input) => {
    if (input.checked) selected.add(input.value);
    else selected.delete(input.value);
  });
  return selected;
}

function update(root) {
  const checked = root.closest("form")?.querySelectorAll("[data-lesson-card] input:checked").length ?? 0;
  const visibleChecked = root.closest("form")?.querySelectorAll("[data-lesson-card]:not([hidden]) input:checked").length ?? checked;
  const total = root.closest("form") ? selectedIDs(root.closest("form")).size : checked;
  const count = root.querySelector("[data-lesson-selected]");
  const hidden = root.querySelector("[data-lesson-hidden]");
  const hiddenCount = root.querySelector("[data-lesson-hidden-count]");
  const submitCount = root.closest("form")?.querySelector("[data-lesson-submit-count]");
  const submit = root.closest("form")?.querySelector(".lesson-picker-start");
  if (count) {
    count.textContent = String(total);
  }
  const offPage = Math.max(0, total - visibleChecked);
  if (hidden) {
    hidden.hidden = offPage === 0;
  }
  if (hiddenCount) {
    hiddenCount.textContent = String(offPage);
  }
  if (submitCount) {
    submitCount.textContent = total === 1 ? "1 word" : `${total} words`;
  }
  if (submit) {
    submit.disabled = total === 0;
  }
}

export function initLessonPicker() {
  const root = document.querySelector("[data-lesson-picker]");
  if (!root) {
    return;
  }
  const form = root.closest("form");
  const grid = form?.querySelector("[data-lesson-grid]");
  if (!grid) {
    return;
  }

  root.querySelector("[data-lesson-filter]")?.addEventListener("input", (event) => {
    const query = event.target.value.trim().toLocaleLowerCase();
    grid.querySelectorAll("[data-lesson-card]").forEach((card) => {
      card.hidden = query !== "" && !card.dataset.lessonSearchText.toLocaleLowerCase().includes(query);
    });
    update(root);
  });
  root.querySelector("[data-lesson-select-visible]")?.addEventListener("click", () => {
    visibleCards(grid).forEach((card) => {
      card.querySelector("input").checked = true;
    });
    update(root);
  });
  root.querySelector("[data-lesson-clear]")?.addEventListener("click", () => {
    const url = new URL(window.location.href);
    url.searchParams.delete("selected");
    window.history.replaceState({}, "", url);
    grid.querySelectorAll("input:checked").forEach((input) => {
      input.checked = false;
    });
    update(root);
  });
  grid.addEventListener("change", () => update(root));

  form.addEventListener("submit", () => {
    const current = new Set(Array.from(form.querySelectorAll('[data-lesson-card] input[name="vocabulary_id"]')).map((input) => input.value));
    selectedIDs(form).forEach((id) => {
      if (current.has(id)) return;
      const hidden = document.createElement("input");
      hidden.type = "hidden";
      hidden.name = "vocabulary_id";
      hidden.value = id;
      form.append(hidden);
    });
  });

  document.querySelectorAll("[data-lesson-pagination] a").forEach((link) => {
    link.addEventListener("click", (event) => {
      event.preventDefault();
      const destination = new URL(link.href);
      destination.searchParams.delete("selected");
      selectedIDs(form).forEach((id) => destination.searchParams.append("selected", id));
      window.location.assign(destination.href);
    });
  });
  update(root);
}
