// wasync's only script. It runs on the two pages that use a passkey from
// another device on the tailnet: setting up this device, and confirming one
// action. Everything it needs is in the form's data attributes; it asks the
// browser for the passkey and posts the browser's answer back in the same form.
// The server checks the answer; this script decides nothing.
"use strict";

(function () {
  var form = document.getElementById("passkey");
  var button = document.getElementById("passkey-go");
  var status = document.getElementById("passkey-status");
  if (!form || !button || !status) {
    return;
  }

  function encode(buffer) {
    var bytes = new Uint8Array(buffer);
    var text = "";
    for (var i = 0; i < bytes.length; i++) {
      text += String.fromCharCode(bytes[i]);
    }
    return btoa(text).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function decode(text) {
    var base64 = text.replace(/-/g, "+").replace(/_/g, "/");
    while (base64.length % 4) {
      base64 += "=";
    }
    var raw = atob(base64);
    var bytes = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) {
      bytes[i] = raw.charCodeAt(i);
    }
    return bytes;
  }

  function credentials(list) {
    if (!list) {
      return [];
    }
    return list.split(",").map(function (id) {
      return { type: "public-key", id: decode(id) };
    });
  }

  function say(text, bad) {
    status.textContent = text;
    status.className = bad ? "bad" : "mut";
  }

  function set(name, value) {
    form.elements.namedItem(name).value = value;
  }

  if (!window.PublicKeyCredential || !navigator.credentials) {
    say("This browser cannot use passkeys. Use a current Safari, Chrome, Edge or Firefox.", true);
    button.disabled = true;
    return;
  }

  var data = form.dataset;
  var creating = data.mode === "create";

  function ask() {
    if (creating) {
      return navigator.credentials.create({
        publicKey: {
          rp: { id: data.rp, name: "wasync" },
          user: { id: decode(data.user), name: data.login, displayName: data.login },
          challenge: decode(data.challenge),
          pubKeyCredParams: [
            { type: "public-key", alg: -7 },
            { type: "public-key", alg: -8 },
            { type: "public-key", alg: -257 },
          ],
          authenticatorSelection: { residentKey: "preferred", userVerification: "required" },
          excludeCredentials: credentials(data.exclude),
          attestation: "none",
          timeout: 120000,
        },
      });
    }
    return navigator.credentials.get({
      publicKey: {
        rpId: data.rp,
        challenge: decode(data.challenge),
        allowCredentials: credentials(data.allow),
        userVerification: "required",
        timeout: 120000,
      },
    });
  }

  button.addEventListener("click", function () {
    button.disabled = true;
    say("Waiting for your passkey…", false);
    ask()
      .then(function (credential) {
        var response = credential.response;
        set("credential", encode(credential.rawId));
        set("client", encode(response.clientDataJSON));
        if (creating) {
          set("attestation", encode(response.attestationObject));
        } else {
          set("authdata", encode(response.authenticatorData));
          set("signature", encode(response.signature));
        }
        say("Checking…", false);
        form.submit();
      })
      .catch(function (error) {
        var name = error && error.name;
        if (creating && name === "InvalidStateError") {
          say("This device already has a passkey for wasync, so it is set up. Nothing changed.", true);
        } else if (name === "NotAllowedError") {
          say("Not confirmed: cancelled, timed out, or no passkey for wasync on this device. Nothing changed. You can try again.", true);
        } else {
          say("The passkey did not work (" + (name || "error") + "). Nothing changed. You can try again.", true);
        }
        button.disabled = false;
      });
  });
})();
