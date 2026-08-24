function updateFileName(input) {
  const picker = input.closest("[data-file-picker]");
  const name = picker?.querySelector("[data-file-name]");
  if (!name) {
    return;
  }
  if (!input.files?.length) {
    name.textContent = input.dataset.emptyLabel || "No file selected";
    return;
  }
  name.textContent = input.files.length === 1 ? input.files[0].name : `${input.files.length} files selected`;
}

export function initFilePickers(root = document) {
  root.querySelectorAll("[data-file-picker] input[type=file]").forEach((input) => {
    input.addEventListener("change", () => updateFileName(input));
    updateFileName(input);
  });
}
