#![cfg_attr(not(feature = "std"), no_std)]

mod envelope;

#[cfg(feature = "runtime-benchmarks")]
mod benchmarking;

pub mod weights;

#[cfg(test)]
mod test;

use codec::DecodeAll;
use frame_support::{
	storage::bounded_btree_set::BoundedBTreeSet,
	traits::fungible::{Inspect, Mutate},
};
use frame_system::ensure_signed;
use snowbridge_core::ParaId;
use sp_core::{ConstU32, H160};
use sp_runtime::traits::AccountIdConversion;
use sp_std::convert::TryFrom;

use envelope::Envelope;
use snowbridge_core::{Message, Verifier};
use snowbridge_router_primitives::{ConvertMessage, Payload};

use xcm::latest::{send_xcm, SendError};

pub use weights::WeightInfo;

#[cfg(feature = "std")]
use sp_std::collections::btree_set::BTreeSet;

use frame_support::{CloneNoBound, EqNoBound, PartialEqNoBound};

use codec::{Decode, Encode};

use scale_info::TypeInfo;

type BalanceOf<T> =
	<<T as Config>::Token as Inspect<<T as frame_system::Config>::AccountId>>::Balance;

type AllowListLength = ConstU32<8>;

#[derive(CloneNoBound, EqNoBound, PartialEqNoBound, Encode, Decode, Debug, TypeInfo)]
pub enum MessageDispatchResult {
	InvalidPayload,
	Dispatched,
	NotDispatched(SendError),
}

pub use pallet::*;

#[frame_support::pallet]
pub mod pallet {

	use super::*;

	use frame_support::{pallet_prelude::*, traits::tokens::Preservation};
	use frame_system::pallet_prelude::*;
	use xcm::v3::SendXcm;
	#[pallet::pallet]
	pub struct Pallet<T>(_);

	#[pallet::config]
	pub trait Config: frame_system::Config {
		type RuntimeEvent: From<Event<Self>> + IsType<<Self as frame_system::Config>::RuntimeEvent>;

		type Verifier: Verifier;

		type Token: Mutate<Self::AccountId>;

		type Reward: Get<BalanceOf<Self>>;

		type MessageConversion: ConvertMessage;

		type XcmSender: SendXcm;

		type WeightInfo: WeightInfo;
	}

	#[pallet::hooks]
	impl<T: Config> Hooks<BlockNumberFor<T>> for Pallet<T> {}

	#[pallet::event]
	#[pallet::generate_deposit(pub(super) fn deposit_event)]
	pub enum Event<T> {
		MessageReceived { dest: ParaId, nonce: u64, result: MessageDispatchResult },
	}

	#[pallet::error]
	pub enum Error<T> {
		/// Message came from an invalid outbound channel on the Ethereum side.
		InvalidOutboundQueue,
		/// Message has an invalid envelope.
		InvalidEnvelope,
		/// Message has an unexpected nonce.
		InvalidNonce,
		/// Cannot convert location
		InvalidAccountConversion,
	}

	#[pallet::storage]
	#[pallet::getter(fn peer)]
	pub type AllowList<T: Config> =
		StorageValue<_, BoundedBTreeSet<H160, AllowListLength>, ValueQuery>;

	#[pallet::storage]
	pub type Nonce<T: Config> = StorageMap<_, Twox64Concat, ParaId, u64, ValueQuery>;

	#[pallet::genesis_config]
	pub struct GenesisConfig {
		pub allowlist: Vec<H160>,
	}

	#[cfg(feature = "std")]
	impl Default for GenesisConfig {
		fn default() -> Self {
			Self { allowlist: Default::default() }
		}
	}

	#[pallet::genesis_build]
	impl<T: Config> GenesisBuild<T> for GenesisConfig {
		fn build(&self) {
			let allowlist: BoundedBTreeSet<H160, AllowListLength> =
				BTreeSet::from_iter(self.allowlist.clone().into_iter())
					.try_into()
					.expect("exceeded bound");
			<AllowList<T>>::put(allowlist);
		}
	}

	#[pallet::call]
	impl<T: Config> Pallet<T> {
		#[pallet::call_index(0)]
		#[pallet::weight({100_000_000})]
		pub fn submit(origin: OriginFor<T>, message: Message) -> DispatchResult {
			let who = ensure_signed(origin)?;
			// submit message to verifier for verification
			let log = T::Verifier::verify(&message)?;

			// Decode log into an Envelope
			let envelope = Envelope::try_from(log).map_err(|_| Error::<T>::InvalidEnvelope)?;

			// Verify that the message was submitted to us from a known
			// outbound channel on the ethereum side
			let allowlist = <AllowList<T>>::get();
			if !allowlist.contains(&envelope.channel) {
				return Err(Error::<T>::InvalidOutboundQueue.into())
			}

			// Verify message nonce
			<Nonce<T>>::try_mutate(envelope.dest, |nonce| -> DispatchResult {
				if envelope.nonce != *nonce + 1 {
					Err(Error::<T>::InvalidNonce.into())
				} else {
					*nonce += 1;
					Ok(())
				}
			})?;

			// Reward relayer from the sovereign account of the destination parachain
			// Expected to fail if sovereign account has no funds
			let sovereign_account = envelope.dest.into_account_truncating();
			T::Token::transfer(&sovereign_account, &who, T::Reward::get(), Preservation::Preserve)?;

			// Dispatch message. From this point, any errors are masked, i.e the extrinsic will
			// succeed even if the message was not successfully dispatched.

			if let Ok(payload) = Payload::decode_all(&mut envelope.payload.as_ref()) {
				let (dest, xcm) =
					T::MessageConversion::convert(envelope.channel, envelope.dest.into(), payload);
				match send_xcm::<T::XcmSender>(dest, xcm) {
					Ok(_) => Self::deposit_event(Event::MessageReceived {
						dest: envelope.dest,
						nonce: envelope.nonce,
						result: MessageDispatchResult::Dispatched,
					}),
					Err(err) => Self::deposit_event(Event::MessageReceived {
						dest: envelope.dest,
						nonce: envelope.nonce,
						result: MessageDispatchResult::NotDispatched(err),
					}),
				}
			} else {
				Self::deposit_event(Event::MessageReceived {
					dest: envelope.dest,
					nonce: envelope.nonce,
					result: MessageDispatchResult::InvalidPayload,
				})
			}

			Ok(())
		}
	}
}