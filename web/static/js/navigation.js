const studyPaths = ["/dashboard", "/reviews", "/lessons", "/study", "/leeches", "/statistics"];

function matchesRoute(path, route) {
  return path === route || path.startsWith(`${route}/`);
}

export function navigationSection(path) {
  if (path === "/" || studyPaths.some((route) => matchesRoute(path, route))) {
    return "study";
  }
  if (matchesRoute(path, "/vocabulary") || matchesRoute(path, "/imports")) {
    return "vocabulary";
  }
  if (matchesRoute(path, "/mining")) {
    return "mining";
  }
  if (matchesRoute(path, "/settings")) {
    return "settings";
  }
  return "";
}
