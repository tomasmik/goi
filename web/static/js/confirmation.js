export function initConfirmations(root, confirmAction) {
  const scope = root || document;
  const ask = confirmAction || ((message) => window.confirm(message));

  scope.querySelectorAll("form[data-confirm], form:has(button[data-confirm])").forEach((form) => {
    if (form.dataset.confirmationBound === "true") {
      return;
    }
    form.dataset.confirmationBound = "true";
    const confirmation = form.querySelector("[data-confirm-field]");
    if (confirmation) {
      confirmation.required = false;
      form.classList.add("confirmation-enhanced");
    }
    form.addEventListener("submit", (event) => {
      const message = event.submitter?.dataset.confirm || form.dataset.confirm;
      if (!message) {
        return;
      }
      if (!ask(message)) {
        event.preventDefault();
        if (confirmation) {
          confirmation.checked = false;
        }
        return;
      }
      if (confirmation) {
        confirmation.checked = true;
      }
    });
  });
}
