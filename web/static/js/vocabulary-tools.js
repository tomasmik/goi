export function initVocabularyTools() {
  const sort = document.querySelector("[data-vocabulary-sort]");
  if (sort) {
    sort.addEventListener("change", () => sort.form?.requestSubmit());
  }
}
