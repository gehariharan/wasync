// Choose chats to sync, a little quicker: with this script a tick is saved
// in the background instead of reloading the page, the Selected panel follows
// at once, and the search runs as you type. Without it the page does the same
// through its buttons and links. The server checks every change; this script
// decides nothing.
"use strict";

(function () {
  var form = document.getElementById("pick");
  if (!form || !window.fetch) {
    return;
  }
  var csrf = form.getAttribute("data-csrf");
  var list = form.querySelector("[data-selected]");
  var count = form.querySelector("[data-count]");

  function post(path, fields) {
    var body = new URLSearchParams();
    body.append("csrf", csrf);
    Object.keys(fields).forEach(function (k) {
      body.append(k, fields[k]);
    });
    return fetch(path, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: body.toString()
    }).then(function (res) {
      if (!res.ok) {
        throw new Error("not saved");
      }
      return res.json();
    });
  }

  function rowFor(chat) {
    return form.querySelector('.pick-row[data-chat="' + CSS.escape(chat) + '"]');
  }

  function showRow(row, on) {
    var name = row.getAttribute("data-name");
    row.setAttribute("aria-pressed", on ? "true" : "false");
    row.name = on ? "stop" : "sync";
    row.setAttribute("aria-label", (on ? "Stop syncing " : "Sync ") + name);
  }

  function addSelected(chat, name, picture) {
    if (!list || list.querySelector('li[data-chat="' + CSS.escape(chat) + '"]')) {
      return;
    }
    var empty = list.querySelector("[data-empty]");
    if (empty) {
      empty.remove();
    }
    var li = document.createElement("li");
    li.setAttribute("data-chat", chat);
    var img = document.createElement("img");
    img.className = "avatar sm";
    img.src = picture;
    img.alt = "";
    img.width = 26;
    img.height = 26;
    var nm = document.createElement("span");
    nm.className = "nm";
    nm.textContent = name;
    var btn = document.createElement("button");
    btn.name = "stop";
    btn.value = chat;
    btn.title = "Remove";
    btn.setAttribute("aria-label", "Remove " + name);
    btn.textContent = "×";
    li.append(img, nm, btn);
    list.prepend(li);
  }

  function removeSelected(chat) {
    var li = list && list.querySelector('li[data-chat="' + CSS.escape(chat) + '"]');
    if (li) {
      li.remove();
    }
  }

  // bypass lets a change the background could not save go to the server
  // the ordinary way, so the page shows why.
  var bypass = false;
  form.addEventListener("submit", function (event) {
    var button = event.submitter;
    if (bypass || !button || (button.name !== "sync" && button.name !== "stop")) {
      return; // Clear, Next and Done go to the server as they are
    }
    event.preventDefault();
    var chat = button.value;
    var on = button.name === "sync";
    var history = form.querySelector('input[name="history"]');
    button.disabled = true;
    post("/ui/api/chats/sync", { chat: chat, sync: on ? "on" : "off", history: history && history.checked ? "yes" : "no" })
      .then(function (answer) {
        var row = rowFor(chat);
        if (row) {
          showRow(row, on);
        }
        if (on) {
          var from = row || button;
          addSelected(chat, from.getAttribute("data-name") || chat, from.getAttribute("data-picture") || "");
        } else {
          removeSelected(chat);
        }
        if (count) {
          count.textContent = answer.syncing;
        }
      })
      .catch(function () {
        bypass = true;
        button.disabled = false;
        form.requestSubmit(button);
      })
      .finally(function () {
        button.disabled = false;
      });
  });

  var media = form.querySelector('input[name="save_media"]');
  if (media) {
    media.addEventListener("change", function () {
      post("/ui/api/settings", { save_media: media.checked ? "on" : "off" }).catch(function () {});
    });
  }

  var search = document.querySelector('input[type="search"][form="pick-search"]');
  var searchForm = document.getElementById("pick-search");
  if (search && searchForm) {
    var timer;
    search.addEventListener("input", function () {
      clearTimeout(timer);
      timer = setTimeout(function () {
        searchForm.requestSubmit();
      }, 450);
    });
    if (search.value) {
      search.focus();
      search.setSelectionRange(search.value.length, search.value.length);
    }
  }
})();
