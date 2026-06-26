import { Icon } from "@iconify-icon/solid";
import { api } from "~/lib/api";
import { toast } from "./toast";

// PTZ controls for ONVIF cameras: press a direction to move, release to stop.
// Pan/tilt/zoom magnitudes match the original (0.6).
export function PtzControls(props: { cameraId: string }) {
  const move = (pan: number, tilt: number, zoom: number) =>
    api(`/cameras/${props.cameraId}/ptz`, {
      method: "POST",
      body: { action: "move", pan, tilt, zoom },
    }).catch((e) => toast((e as Error).message, "error"));

  const stop = () =>
    api(`/cameras/${props.cameraId}/ptz`, { method: "POST", body: { action: "stop" } }).catch(
      (e) => toast((e as Error).message, "error"),
    );

  const press = (pan: number, tilt: number, zoom: number) => ({
    onMouseDown: () => move(pan, tilt, zoom),
    onMouseUp: () => stop(),
    onMouseLeave: () => stop(),
  });

  return (
    <div>
      <div class="grid grid-cols-3 gap-1.5 w-[140px] mx-auto">
        <span />
        <button class="btn btn-sm btn-neutral" {...press(0, 0.6, 0)}>
          <Icon icon="lucide:chevron-up" />
        </button>
        <span />
        <button class="btn btn-sm btn-neutral" {...press(-0.6, 0, 0)}>
          <Icon icon="lucide:chevron-left" />
        </button>
        <button class="btn btn-sm btn-neutral" onClick={() => stop()}>
          <Icon icon="lucide:square" />
        </button>
        <button class="btn btn-sm btn-neutral" {...press(0.6, 0, 0)}>
          <Icon icon="lucide:chevron-right" />
        </button>
        <span />
        <button class="btn btn-sm btn-neutral" {...press(0, -0.6, 0)}>
          <Icon icon="lucide:chevron-down" />
        </button>
        <span />
      </div>
      <div class="flex gap-2 mt-2">
        <button class="btn btn-sm flex-1" {...press(0, 0, 0.6)}>
          放大 +
        </button>
        <button class="btn btn-sm flex-1" {...press(0, 0, -0.6)}>
          缩小 −
        </button>
      </div>
    </div>
  );
}
