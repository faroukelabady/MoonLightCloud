import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/svelte";
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import DeviceManagementDialog from "./DeviceManagementDialog.svelte";
import { dashboardApi, ApiError } from "../lib/api";
import { setLang } from "../lib/i18n.svelte";

const storeID = "11111111-1111-4111-8111-111111111111";
const deviceID = "22222222-2222-4222-8222-222222222222";
const credentialID = "33333333-3333-4333-8333-333333333333";
const secret = `${deviceID}.${credentialID}.${"a".repeat(64)}`;
const provision = {
  device_id: deviceID,
  credential_id: credentialID,
  name: "Shop PC",
  store_id: storeID,
  credential: secret,
};
beforeEach(() => setLang("en"));
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
async function approve() {
  await fireEvent.input(screen.getByLabelText("Authenticator code"), {
    target: { value: "123456" },
  });
  await fireEvent.submit(screen.getByRole("dialog").querySelector("form")!);
}
it("requires an OTP, shows a credential once, copies it and clears it on close", async () => {
  const create = vi
    .spyOn(dashboardApi, "createDevice")
    .mockResolvedValue(provision);
  const copied = vi.fn(async () => {});
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText: copied },
  });
  const close = vi.fn();
  const view = render(DeviceManagementDialog, {
    kind: "device",
    storeID,
    storeName: "Shop",
    onclose: close,
    onchanged: vi.fn(),
  });
  expect(
    (screen.getByRole("button", { name: "Create device" }) as HTMLButtonElement)
      .disabled,
  ).toBe(true);
  expect(create).not.toHaveBeenCalled();
  await fireEvent.input(screen.getByLabelText("Device name"), {
    target: { value: "Shop PC" },
  });
  await approve();
  const field = await screen.findByLabelText("Device credential");
  expect((field as HTMLTextAreaElement).value).toBe(secret);
  expect(field.hasAttribute("readonly")).toBe(true);
  expect(create).toHaveBeenCalledWith({
    store_id: storeID,
    name: "Shop PC",
    mfa_code: "123456",
  });
  await fireEvent.click(
    screen.getByRole("button", { name: "Copy credential" }),
  );
  expect(copied).toHaveBeenCalledWith(secret);
  expect(localStorage.getItem("credential")).toBeNull();
  expect(sessionStorage.getItem("credential")).toBeNull();
  await fireEvent.click(
    screen.getByRole("button", { name: "I have copied it" }),
  );
  expect(close).toHaveBeenCalled();
  expect(screen.queryByDisplayValue(secret)).toBeNull();
  view.unmount();
  render(DeviceManagementDialog, {
    kind: "device",
    storeID,
    onclose: vi.fn(),
    onchanged: vi.fn(),
  });
  expect(screen.queryByDisplayValue(secret)).toBeNull();
  expect(create).toHaveBeenCalledTimes(1);
});
it("regenerates an existing device only after fresh approval", async () => {
  const rotate = vi
    .spyOn(dashboardApi, "rotateDevice")
    .mockResolvedValue(provision);
  render(DeviceManagementDialog, {
    kind: "rotate",
    deviceID,
    deviceName: "Shop PC",
    onclose: vi.fn(),
    onchanged: vi.fn(),
  });
  expect(rotate).not.toHaveBeenCalled();
  expect(screen.getByText(/old credential will stop working/)).toBeTruthy();
  await approve();
  expect(
    ((await screen.findByLabelText("Device credential")) as HTMLTextAreaElement)
      .value,
  ).toBe(secret);
  expect(rotate).toHaveBeenCalledWith(deviceID, "123456");
});
it("clears rejected OTPs and does not reveal credentials", async () => {
  vi.spyOn(dashboardApi, "rotateDevice").mockRejectedValue(
    new ApiError(403, "FORBIDDEN", "denied", "MFA_INVALID"),
  );
  render(DeviceManagementDialog, {
    kind: "rotate",
    deviceID,
    onclose: vi.fn(),
    onchanged: vi.fn(),
  });
  await approve();
  expect((await screen.findByRole("alert")).textContent).toContain(
    "invalid or already used",
  );
  expect(
    (screen.getByLabelText("Authenticator code") as HTMLInputElement).value,
  ).toBe("");
  expect(screen.queryByLabelText("Device credential")).toBeNull();
});
it("creates a Cloud Store using the ID copied from Retail", async () => {
  const create = vi
    .spyOn(dashboardApi, "createStore")
    .mockResolvedValue({ store_id: storeID } as never);
  const close = vi.fn();
  render(DeviceManagementDialog, {
    kind: "store",
    onclose: close,
    onchanged: vi.fn(),
  });
  await fireEvent.input(screen.getByLabelText("Store name"), {
    target: { value: "Shop" },
  });
  await fireEvent.input(screen.getByLabelText("Retail Store ID"), {
    target: { value: storeID },
  });
  await approve();
  await waitFor(() => expect(close).toHaveBeenCalled());
  expect(create).toHaveBeenCalledWith({
    store_id: storeID,
    display_name: "Shop",
    timezone: "Africa/Cairo",
    mfa_code: "123456",
  });
});
it("requires approval before revocation and keeps identifiers LTR in Arabic", async () => {
  setLang("ar");
  const revoke = vi
    .spyOn(dashboardApi, "revokeDevice")
    .mockResolvedValue({ revoked: true });
  render(DeviceManagementDialog, {
    kind: "revoke",
    deviceID,
    onclose: vi.fn(),
    onchanged: vi.fn(),
  });
  expect(revoke).not.toHaveBeenCalled();
  const otp = screen.getByLabelText("رمز تطبيق المصادقة");
  expect(otp.getAttribute("dir")).toBe("ltr");
  await fireEvent.input(otp, { target: { value: "123456" } });
  await fireEvent.submit(screen.getByRole("dialog").querySelector("form")!);
  await waitFor(() => expect(revoke).toHaveBeenCalledWith(deviceID, "123456"));
});
