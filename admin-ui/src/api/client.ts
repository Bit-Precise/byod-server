import createClient from "openapi-fetch";
import type { paths } from "./generated";

export const api = createClient<paths>({
  baseUrl: window.location.origin,
  credentials: "include",
});

api.use({
  onRequest({ request }) {
    const token = window.localStorage.getItem("byod.admin_token");
    if (token) request.headers.set("X-Admin-Token", token);
    return request;
  },
});
