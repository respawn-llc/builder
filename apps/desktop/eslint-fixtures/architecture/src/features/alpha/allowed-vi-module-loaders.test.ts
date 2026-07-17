import { vi } from "vitest";

vi.mock("@/app-facade");
vi.doMock("@/app-facade");
vi.unmock("@/app-facade");
vi.doUnmock("@/app-facade");
void vi.importActual("@/app-facade");
void vi.importMock("@/app-facade");
