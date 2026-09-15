/** Section is the console's top-level navigation. The app switches on it
 *  rather than using a router: there are a handful of screens, all behind one
 *  login, and a router would add a dependency without adding anything a user
 *  notices. */
export type Section =
  | "traffic"
  | "hosts"
  | "redirects"
  | "access-lists"
  | "access-log"
  | "certificates"
  | "account"

export const SECTION_TITLES: Record<Section, string> = {
  traffic: "Traffic",
  hosts: "Hosts",
  redirects: "Redirects",
  "access-lists": "Access lists",
  "access-log": "Access log",
  certificates: "Certificates",
  account: "Account",
}
