(function () {
  const theme = document.cookie.split("; ").find((part) => part.startsWith("goi_theme="))?.split("=")[1];
  const dark = theme === "dark" || (theme === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  if (dark) {
    document.documentElement.classList.add("theme-dark");
  }
}());
