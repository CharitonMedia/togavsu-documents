// The frame bridge (CC-6): ask the core page that frames this one to open the editor or
// navigate. The core checks each request as the viewer.
document.addEventListener("click", function (e) {
  var ed = e.target.closest("[data-editor]");
  if (ed) {
    e.preventDefault();
    window.parent.postMessage({ type: "togavsu:editor.open", file_id: ed.getAttribute("data-editor") }, "*");
    return;
  }
  var nav = e.target.closest("[data-navigate]");
  if (nav && window.parent !== window) {
    e.preventDefault();
    window.parent.postMessage({ type: "togavsu:navigate", page: nav.getAttribute("data-navigate"), params: { matter_id: nav.getAttribute("data-matter") } }, "*");
  }
});
