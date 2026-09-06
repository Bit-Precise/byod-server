import createClient from "openapi-fetch";
import type { paths } from "./generated";

export const api = createClient<paths>({
  baseUrl: window.location.origin,
  credentials: "include",
});

api.use({
  onRequest({ request }) {
    const token = window.localStorage.getItem("byod.admin_token");
    // Fetch forbids non-Latin-1 header values. Admin tokens are generated as
    // URL-safe ASCII strings; reject anything pasted with Unicode whitespace,
    // labels, or other non-token characters before Headers.set can throw.
    if (token && /^[\x21-\x7e]+$/.test(token.trim())) {
      request.headers.set("X-Admin-Token", token.trim());
    } else if (token) {
      window.localStorage.removeItem("byod.admin_token");
      window.dispatchEvent(new CustomEvent("byod:invalid-token"));
    }
    return request;
  },
});
