import createClient from "openapi-fetch";
import type { paths } from "./generated";

export const api = createClient<paths>({
  baseUrl: window.location.origin,
  credentials: "include",
});

api.use({
  onRequest({ request }) {
    const csrf = window.localStorage.getItem("byod.csrf_token");
    if (csrf && /^[\x21-\x7e]+$/.test(csrf.trim())) request.headers.set("X-CSRF-Token", csrf.trim());
    return request;
  },
});
