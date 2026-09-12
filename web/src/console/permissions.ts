import { computed } from "vue";
import { recoveryState } from "./recovery";
import { currentUser, isAuthenticated } from "../api";
export const isAdmin = computed(
  () => recoveryState.value === "normal" && isAuthenticated.value && currentUser.value.role === "admin",
);
export const canOperate = computed(
  () =>
    recoveryState.value === "normal" && isAuthenticated.value &&
    ["admin", "operator"].includes(currentUser.value.role),
);
