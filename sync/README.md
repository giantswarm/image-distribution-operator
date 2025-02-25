# Keeping the chart up to date and preserving GS specific configuration

The `sync.sh` script is used to keep the chart up to date with the kubebuilder config and to preserve Giant Swarm specific changes.
We use `kubebuilder edit` andd `vendir` to generate the chart and `git patch` to apply the Giant Swarm specific changes.

## How to update the chart

Since this is a `kubebuilder` project, the manifests are kept in the `config` directory.
Please apply changes directly to the manifests in the `config` directory.
In order to update the chart, you need to run the following commands:

1. Run `kubebuilder edit --plugins=helm/v1-alpha` to generate the chart from the `config` directory.
2. Run `vendir sync` to update the chart inside the `helm` directory.

## How to maintain Giant Swarm specific changes to the chart

This folder contains the `sync.sh` script which does the following:

- Updates the chart from `config` directory.
- Applies all patches in the `patches` directory to the chart.

Generally making changes to `config` and running the script should be enough to keep the chart up to date.

1. Ensure all files that should be ignored are listed in the `vendir.yml` file.
2. Run `./sync.sh`

However, if changes need to be applied after the chart has been generated it is done by creating a patch.
(e.g changes to _helpers.tpl)

## How to generate a patch

Patches are simply git diffs of the changes made to the upstream chart.

1. Update the chart. (see above steps)
2. Commit only the manifest that you want to generate a patch for. (inside the `helm` directory)
3. Make the Giant Swarm specific changes to the manifest.
4. Run `git diff helm/PATH/TO/MANIFEST > sync/patches/NAME.patch`
5. Run `./sync.sh` to apply all patches.
