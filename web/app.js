const $ = (selector) => document.querySelector(selector);
const result = $("#result");
let activePrivateKey;
let activeKeyMetadata;

function bytesToBase64(bytes) {
  let binary = "";
  for (const byte of new Uint8Array(bytes)) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function base64ToBytes(value) {
  return Uint8Array.from(atob(value.trim()), (character) => character.charCodeAt(0));
}

function appendVarint(target, value) {
  let remaining = BigInt.asUintN(64, BigInt(value));
  while (remaining > 127n) {
    target.push(Number((remaining & 127n) | 128n));
    remaining >>= 7n;
  }
  target.push(Number(remaining));
}

function appendBytes(target, field, bytes) {
  appendVarint(target, BigInt(field * 8 + 2));
  appendVarint(target, BigInt(bytes.length));
  for (const byte of bytes) target.push(byte);
}

function appendString(target, field, value) {
  if (value) appendBytes(target, field, new TextEncoder().encode(value));
}

function canonicalReading(reading) {
  const target = [];
  appendString(target, 1, reading.readingId);
  appendString(target, 2, reading.householdId);
  appendString(target, 3, reading.meterId);
  const instant = new Date(reading.intervalStart);
  const timestamp = [];
  const seconds = BigInt(Math.floor(instant.getTime() / 1000));
  const nanos = (instant.getTime() % 1000) * 1_000_000;
  if (seconds !== 0n) {
    appendVarint(timestamp, 8n);
    appendVarint(timestamp, seconds);
  }
  if (nanos !== 0) {
    appendVarint(timestamp, 16n);
    appendVarint(timestamp, nanos);
  }
  appendBytes(target, 4, timestamp);
  const produced = BigInt(reading.producedEnergyWh);
  const consumed = BigInt(reading.consumedEnergyWh);
  if (produced !== 0n) {
    appendVarint(target, 40n);
    appendVarint(target, produced);
  }
  if (consumed !== 0n) {
    appendVarint(target, 48n);
    appendVarint(target, consumed);
  }
  return new Uint8Array(target);
}

function show(message, isError = false) {
  result.textContent = message;
  result.classList.toggle("error", isError);
  result.classList.remove("hidden");
  result.scrollIntoView({ behavior: "smooth", block: "nearest" });
}

function nextIntervalLocal() {
  const date = new Date(Math.ceil(Date.now() / 300_000) * 300_000);
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function makeKeyId() {
  return `medidor-${crypto.randomUUID().slice(0, 8)}`;
}

async function generateKey() {
  if (!crypto.subtle || !crypto.randomUUID) throw new Error("Usa un navegador moderno con HTTPS para crear la llave.");
  const pair = await crypto.subtle.generateKey({ name: "Ed25519" }, true, ["sign", "verify"]);
  const publicKey = bytesToBase64(await crypto.subtle.exportKey("raw", pair.publicKey));
  const privateKey = bytesToBase64(await crypto.subtle.exportKey("pkcs8", pair.privateKey));
  const householdId = $("#household").value.trim();
  const keyId = makeKeyId();
  if (!householdId) throw new Error("Escribe el identificador del hogar antes de generar la llave.");
  activePrivateKey = pair.privateKey;
  activeKeyMetadata = { key_id: keyId, household_id: householdId, public_key: publicKey, private_key: privateKey };
  $("#key-id").value = keyId;
  $("#private-key").value = privateKey;
  $("#public-config").textContent = JSON.stringify({
    [keyId]: { household_id: householdId, public_key: publicKey },
  }, null, 2);
  $("#key-panel").classList.remove("hidden");
}

async function loadPrivateKey(file) {
  const imported = JSON.parse(await file.text());
  if (!imported.private_key || !imported.key_id || !imported.household_id) {
    throw new Error("El archivo no contiene key_id, household_id y private_key.");
  }
  activePrivateKey = await crypto.subtle.importKey(
    "pkcs8", base64ToBytes(imported.private_key), { name: "Ed25519" }, false, ["sign"],
  );
  activeKeyMetadata = imported;
  $("#key-id").value = imported.key_id;
  $("#household").value = imported.household_id;
  $("#private-key").value = imported.private_key;
  $("#key-panel").classList.add("hidden");
}

async function checkBackend() {
  try {
    const response = await fetch(`${window.VOLTBARRIO_API_BASE_URL}/healthz`, { cache: "no-store" });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    $("#health-dot").className = "dot good";
    $("#health-text").textContent = "Backend y base de datos disponibles";
  } catch {
    $("#health-dot").className = "dot bad";
    $("#health-text").textContent = "Backend no disponible; revisa su estado en Render";
  }
}

async function submitReading(event) {
  event.preventDefault();
  const button = $("#submit-reading");
  try {
    const householdId = $("#household").value.trim();
    const keyId = $("#key-id").value.trim();
    if (!activePrivateKey || !activeKeyMetadata) throw new Error("Genera o carga la llave Ed25519 primero.");
    if (activeKeyMetadata.household_id !== householdId || activeKeyMetadata.key_id !== keyId) {
      throw new Error("La llave cargada no corresponde al hogar e identificador seleccionados.");
    }
    const localInterval = $("#interval").value;
    if (!localInterval) throw new Error("Selecciona el intervalo de cinco minutos.");
    const instant = new Date(localInterval);
    if (instant.getSeconds() !== 0 || instant.getMilliseconds() !== 0 || instant.getMinutes() % 5 !== 0) {
      throw new Error("El intervalo debe empezar en un minuto divisible entre cinco.");
    }
    const reading = {
      readingId: crypto.randomUUID(),
      householdId,
      meterId: $("#meter-id").value.trim(),
      intervalStart: instant.toISOString(),
      producedEnergyWh: $("#produced").value,
      consumedEnergyWh: $("#consumed").value,
    };
    if (!reading.meterId || BigInt(reading.producedEnergyWh) < 0n || BigInt(reading.consumedEnergyWh) < 0n) {
      throw new Error("Revisa el medidor y los valores de energía.");
    }
    button.disabled = true;
    button.textContent = "Firmando y enviando…";
    const signature = await crypto.subtle.sign({ name: "Ed25519" }, activePrivateKey, canonicalReading(reading));
    const response = await fetch(`${window.VOLTBARRIO_API_BASE_URL}/v1/readings:batch`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        readings: [{
          reading: {
            readingId: reading.readingId,
            householdId: reading.householdId,
            meterId: reading.meterId,
            intervalStart: reading.intervalStart,
            producedEnergyWh: reading.producedEnergyWh,
            consumedEnergyWh: reading.consumedEnergyWh,
          },
          keyId,
          signature: bytesToBase64(signature),
        }],
      }),
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.message || body.error || `El backend respondió HTTP ${response.status}.`);
    const duplicate = Number(body.duplicateCount || 0) > 0;
    show(duplicate
      ? `Lectura ${reading.readingId} ya estaba registrada; no se duplicó.`
      : `Lectura ${reading.readingId} aceptada. Producción ${reading.producedEnergyWh} Wh; consumo ${reading.consumedEnergyWh} Wh.`);
  } catch (error) {
    show(error.message || "No se pudo enviar la lectura.", true);
  } finally {
    button.disabled = false;
    button.innerHTML = 'Firmar y enviar lectura <span aria-hidden="true">↗</span>';
  }
}

$("#generate-key").addEventListener("click", () => generateKey().catch((error) => show(error.message, true)));
$("#key-file").addEventListener("change", (event) => {
  const [file] = event.target.files;
  if (file) loadPrivateKey(file).catch((error) => show(error.message, true));
});
$("#download-key").addEventListener("click", () => {
  if (!activeKeyMetadata) return;
  const blob = new Blob([JSON.stringify(activeKeyMetadata, null, 2)], { type: "application/json" });
  const link = document.createElement("a");
  link.href = URL.createObjectURL(blob);
  link.download = `${activeKeyMetadata.key_id}-private.json`;
  link.click();
  URL.revokeObjectURL(link.href);
});
$("#copy-public").addEventListener("click", async () => {
  try {
    await navigator.clipboard.writeText($("#public-config").textContent);
    show("Configuración pública copiada. Agrégala al valor MEASUREMENT_PUBLIC_KEYS en Render.");
  } catch {
    show("No se pudo copiar automáticamente; selecciona y copia el bloque JSON.", true);
  }
});
$("#reading-form").addEventListener("submit", submitReading);
$("#interval").value = nextIntervalLocal();
checkBackend();
